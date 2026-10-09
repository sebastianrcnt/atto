package atto.swing;

import java.util.*;
import java.util.concurrent.*;
import java.util.function.Consumer;
import static atto.swing.Json.*;

/** UI-free session store. All mutations and callbacks use the protocol dispatch thread. */
public final class Core implements AutoCloseable {
    public final Protocol protocol;
    final Map<String, ThreadState> threads = new LinkedHashMap<>();
    final List<Map<String, Object>> backlog = new ArrayList<>();
    final Set<String> paging = new HashSet<>();
 final Set<String> hydrating = new HashSet<>();
    final Set<String> dirty = new HashSet<>();
    boolean updateScheduled;
    long operation, backlogFloor;
    int backlogBytes;
    final Map<String, Long> detachedAt = new HashMap<>();
    public Consumer<Map<String, Object>> changed = t -> {};
    public Consumer<Map<String, Object>> event = n -> {};
    public Consumer<String> error = e -> {};
    public Consumer<String> connection = s -> {};
    public Consumer<Map<String, Object>> opened = t -> {};
    public Core(Options options) { this(options, Transport::open); }
    Core(Options options, Protocol.Connector connector) {
        protocol = new Protocol(options, connector);
        protocol.state = s -> connection.accept(s);
        protocol.connected = () -> {
            backlog.clear(); backlogBytes = 0; backlogFloor = 0;
            for (String id : List.copyOf(threads.keySet())) hydrate("thread/resume", map("threadId", id));
        };
        protocol.notifications = this::notification;
    }
    public CompletableFuture<Void> connect() { return protocol.connect(); }
    void notification(Map<String, Object> n) {
        backlog.add(n); backlogBytes += write(n).length();
        if (backlog.size() > 10000 || backlogBytes > 2 * 1024 * 1024) {
            for (var event : backlog) backlogFloor = Math.max(backlogFloor, num(event.get("eventId")));
            backlog.clear(); backlogBytes = 0; threads.values().forEach(t -> t.needsSnapshot = true);
        }
        for (var t : threads.values()) {
            if (hydrating.contains(str(t.info.get("threadId")))) continue;
            if (t.apply(n)) {
                dirty.add(str(t.info.get("threadId")));
                if (!updateScheduled) {
                    updateScheduled = true;
                    protocol.dispatch.schedule(() -> {
                        updateScheduled = false;
                        for (String id : List.copyOf(dirty)) { ThreadState state = threads.get(id); if (state != null) changed.accept(state.projection()); }
                        dirty.clear();
                    }, 50, TimeUnit.MILLISECONDS);
                }
            }
            if (t.needsSnapshot && !yes(t.info.get("offline")) && !yes(t.info.get("closed")) && !hydrating.contains(str(t.info.get("threadId")))) hydrateOnLane("thread/read", map("threadId", t.info.get("threadId")));
        }
        if ("thread/closed".equals(n.get("method"))) {
            String id = str(obj(n.get("params")).get("threadId")); ThreadState closed = threads.remove(id);
            if (closed != null) changed.accept(closed.projection());
            detachedAt.put(id, ++operation);
        }
        event.accept(n);
    }
    public CompletableFuture<Map<String, Object>> hydrate(String method, Map<String, Object> params) {
        CompletableFuture<Map<String, Object>> result = new CompletableFuture<>();
        protocol.dispatch.execute(() -> hydrateOnLane(method, params).whenComplete((v, e) -> {
            if (e != null) result.completeExceptionally(e); else result.complete(v);
        }));
        return result;
    }
    private CompletableFuture<Map<String, Object>> hydrateOnLane(String method, Map<String, Object> params) {
        String id = str(params.get("threadId")); long epoch = ++operation; hydrating.add(id);
        return protocol.call(method, params).exceptionallyCompose(e -> {
            Throwable cause = e instanceof CompletionException ? e.getCause() : e;
            if (method.equals("thread/resume") && cause instanceof Protocol.RpcError rpc && rpc.reason().equals("ownedByLegacyWriter")) {
                return protocol.call("thread/read", map("threadId", id, "offline", true)).thenApply(value -> {
                    Map<String, Object> snapshot = new LinkedHashMap<>(obj(value));
                    snapshot.put("readOnly", rpc.getMessage()); snapshot.put("offline", true); return snapshot;
                });
            }
            return CompletableFuture.failedFuture(e);
        }).thenApplyAsync(value -> {
            Map<String, Object> snapshot = obj(value); String actual = str(snapshot.get("threadId"));
            if (actual.isEmpty()) throw new IllegalArgumentException("Snapshot has no threadId");
            if (detachedAt.getOrDefault(actual, 0L) > epoch) throw new CancellationException("Session detached while opening");
            ThreadState t = threads.computeIfAbsent(actual, k -> new ThreadState()); t.reset(snapshot);
            long boundary = t.cursor;
            if (boundary < backlogFloor) t.needsSnapshot = true;
            for (var n : backlog) if (num(n.get("eventId")) > boundary) t.apply(n);
            hydrating.remove(id); changed.accept(t.snapshot()); if (!method.equals("thread/read")) opened.accept(t.snapshot());
            if (t.needsSnapshot) hydrateOnLane("thread/read", map("threadId", actual));
            return t.snapshot();
        }, protocol.dispatch).whenCompleteAsync((v, e) -> { if (e != null) { hydrating.remove(id); if (!(e instanceof CancellationException) && !(e.getCause() instanceof CancellationException)) error.accept(e.getMessage()); } }, protocol.dispatch);
    }
    public CompletableFuture<Map<String, Object>> loadEarlier(String id) {
        CompletableFuture<Map<String, Object>> result = new CompletableFuture<>();
        protocol.dispatch.execute(() -> {
            ThreadState t = threads.get(id);
            if (t == null || !yes(t.info.get("hasMore")) || !paging.add(id)) { result.complete(Map.of()); return; }
            long generation = t.generation;
            protocol.call("thread/items", map("threadId", id, "before", t.info.get("before"), "offline", yes(t.info.get("offline")), "limit", 200))
                .whenCompleteAsync((value, failure) -> {
                    paging.remove(id);
                    if (failure != null) { result.completeExceptionally(failure); error.accept(failure.getMessage()); return; }
                    if (threads.get(id) != t || generation != t.generation) { result.complete(Map.of()); return; }
                    t.prepend(obj(value)); changed.accept(t.projection()); result.complete(obj(value));
                }, protocol.dispatch);
        });
        return result;
    }
    public CompletableFuture<Object> call(String method, String id, Map<String, Object> fields) {
        Map<String, Object> params = new LinkedHashMap<>(fields);
        if (!id.isEmpty()) params.put("threadId", id);
        return protocol.call(method, params).thenApplyAsync(value -> {
            if (method.equals("thread/close") || method.equals("thread/archive")) threads.remove(id);
            return value;
        }, protocol.dispatch);
    }
    public void detach(String id) {
        protocol.dispatch.execute(() -> {
            threads.remove(id); hydrating.remove(id); detachedAt.put(id, ++operation);
            call("thread/detach", id, map("reason", "exit")).exceptionally(e -> null);
        });
    }
    public void close() { protocol.close(); }
}
