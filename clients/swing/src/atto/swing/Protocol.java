package atto.swing;

import java.io.IOException;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Consumer;
import static atto.swing.Json.*;

/** Ordered inbound dispatch, non-blocking requests, and reconnect without input replay. */
public final class Protocol implements AutoCloseable {
    @FunctionalInterface interface Connector {
        Transport open(Options options, Consumer<String> message, Consumer<Throwable> lost) throws Exception;
    }
    final Options options;
    final Connector connector;
    final ScheduledExecutorService dispatch = Executors.newSingleThreadScheduledExecutor(r -> {
        Thread t = new Thread(r, "atto-protocol"); t.setDaemon(true); return t;
    });
    final ExecutorService writes = Executors.newSingleThreadExecutor(Thread.ofVirtual().factory());
    final Map<Long, CompletableFuture<Object>> pending = new ConcurrentHashMap<>();
    final AtomicLong sequence = new AtomicLong();
    volatile Transport transport; volatile boolean closed, ready;
    volatile long generation;
    public volatile String clientId = "", serverInstanceId = "";
    public Consumer<Map<String, Object>> notifications = n -> {};
    public Consumer<String> state = s -> {};
    public Runnable connected = () -> {};
    private CompletableFuture<Void> first = new CompletableFuture<>();

    public Protocol(Options options) { this(options, Transport::open); }
    Protocol(Options options, Connector connector) { this.options = options; this.connector = connector; }
    public CompletableFuture<Void> connect() { dispatch.execute(this::open); return first; }
    void open() {
        if (closed) return;
        ready = false; state.accept("Connecting…");
        long gen = ++generation;
        Thread.ofVirtual().start(() -> {
            try {
                Transport next = connector.open(options,
                    line -> dispatch.execute(() -> { if (gen == generation && !closed) {
                        try { receive(line); } catch (Throwable e) { disconnected(gen, e); }
                    } }),
                    error -> dispatch.execute(() -> disconnected(gen, error)));
                dispatch.execute(() -> {
                    if (closed || gen != generation) { next.close(); return; }
                    transport = next;
                    call("initialize", map("protocolVersions", List.of(3, 2, 1),
                        "clientInfo", map("name", "atto-swing", "title", "atto Swing", "version", "1"),
                        "capabilities", map("interactive", true, "images", true))).whenCompleteAsync((v, e) -> {
                            if (e != null) { disconnected(gen, e); return; }
                            Map<String, Object> init = obj(v);
                            clientId = str(init.get("clientId")); serverInstanceId = str(init.get("serverInstanceId"));
                            notify("initialized", Map.of()); ready = true; state.accept("Connected");
                            connected.run(); first.complete(null);
                        }, dispatch);
                });
            } catch (Throwable e) { dispatch.execute(() -> disconnected(gen, e)); }
        });
    }
    void receive(String line) {
        Map<String, Object> n = obj(parse(line));
        if (n.containsKey("id")) {
            CompletableFuture<Object> f = pending.remove(num(n.get("id")));
            if (f != null) {
                if (n.containsKey("error")) f.completeExceptionally(new RpcError(obj(n.get("error"))));
                else f.complete(n.get("result"));
            }
        } else if (n.containsKey("method")) notifications.accept(n);
    }
    void disconnected(long gen, Throwable e) {
        if (closed || gen != generation) return;
        generation++; ready = false;
        Transport old = transport; transport = null;
        if (old != null) Thread.ofVirtual().start(old::close);
        var failure = new IOException("Disconnected; accepted requests will not be replayed", e);
        pending.values().forEach(f -> f.completeExceptionally(failure)); pending.clear();
        state.accept("Disconnected: " + e.getMessage() + " · retrying in 2s");
        dispatch.schedule(this::open, 2, TimeUnit.SECONDS);
    }
    public CompletableFuture<Object> call(String method, Map<String, Object> params) {
        long id = sequence.incrementAndGet();
        CompletableFuture<Object> f = new CompletableFuture<>();
        pending.put(id, f);
        send(map("jsonrpc", "2.0", "id", id, "method", method, "params", params), f);
        f.orTimeout(60, TimeUnit.SECONDS).whenComplete((v, e) -> pending.remove(id));
        return f;
    }
    public void notify(String method, Map<String, Object> params) { send(map("method", method, "params", params), null); }
    void send(Map<String, Object> message, CompletableFuture<Object> f) {
        Transport target = transport;
        if (closed || target == null) {
            if (f != null) f.completeExceptionally(new IOException("Not connected")); return;
        }
        writes.execute(() -> {
            try { target.send(write(message)); }
            catch (Throwable e) { dispatch.execute(() -> { if (f != null) f.completeExceptionally(e); if (target == transport) disconnected(generation, e); }); }
        });
    }
    public void reconnect() { dispatch.execute(() -> disconnected(generation, new IOException("Reconnect requested"))); }
    public void close() {
        closed = true; ready = false;
        first.completeExceptionally(new IOException("Client detached"));
        Transport old = transport; transport = null;
        if (old != null) Thread.ofVirtual().start(old::close);
        pending.values().forEach(f -> f.completeExceptionally(new IOException("Client detached"))); pending.clear();
        writes.shutdown(); dispatch.shutdown();
    }
    public static final class RpcError extends RuntimeException {
        public final Map<String, Object> error;
        RpcError(Map<String, Object> error) { super(str(error.get("message"))); this.error = error; }
        public String reason() { return str(obj(error.get("data")).get("reason")); }
    }
}
