package atto.swing;

import java.io.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import java.util.function.Consumer;
import static atto.swing.Json.*;

public final class Tests {
    static int checks;
    static void check(boolean value, String message) { checks++; if (!value) throw new AssertionError(message); }
    static void rejects(Runnable action) {
        try { action.run(); throw new AssertionError("Expected rejection"); } catch (IllegalArgumentException expected) { checks++; }
    }
    public static void main(String[] args) throws Exception {
        json(); reducer(); markdown(); protocol(); lines(); core(); preferences();
        System.out.println("Swing core tests passed (" + checks + " assertions)");
    }
    static void json() {
        for (String text : List.of("null", "true", "false", "0", "-42", "1.25e-4", "9223372036854775808", "\"hello\\n😀\\u0041\"", "{\"x\":[null,1,true,\"y\"]}")) {
            Object v = parse(text); check(write(parse(write(v))).equals(write(v)), "Round trip " + text);
        }
        for (String text : List.of("", "01", "-", "1.", "1e", "1e+", "[1,]", "{\"a\":1,}", "{\"a\":1,\"a\":2}", "[", "\"\\q\"", "\"\n\"", "true x", "+1", ".5", "NaN", "\"\\u+001\"", "\"\\u-001\"", "\"\\uZZZZ\"", "\"\\u123\"")) rejects(() -> parse(text));
        rejects(() -> parse("[".repeat(130) + "0" + "]".repeat(130)));
        rejects(() -> write(Double.NaN)); rejects(() -> write(Map.of(1, "a"))); rejects(() -> write(new Object()));
        String controls = "\u0000\b\f\n\r\t\\\"/"; check(str(parse(write(controls))).equals(controls), "String controls");
        check(num(parse("9007199254740993")) == 9007199254740993L, "Integer precision");
    }
    static Map<String, Object> notification(long id, String method, Object... fields) {
        Map<String, Object> p = map(fields); p.put("threadId", "t"); return map("eventId", id, "method", method, "params", p);
    }
    static Map<String, Object> item(String id, String type, String text) { return map("id", id, "type", type, "text", text, "status", "inProgress"); }
    static void reducer() {
        ThreadState state = new ThreadState(); Map<String, Object> snap = map("threadId", "t", "eventId", 10, "serverInstanceId", "one", "items", List.of(item("a", "agentMessage", "He")));
        state.reset(snap); Map<String, Object> delta = notification(11, "item/delta", "itemId", "a", "delta", "llo");
        check(state.apply(delta), "Fresh delta"); check(!state.apply(delta), "Duplicate delta"); check(str(state.items.get("a").get("text")).equals("Hello"), "Exactly once text");
        check(!state.apply(notification(9, "item/delta", "itemId", "a", "delta", "old")), "Snapshot boundary");
        Map<String, Object> other = notification(12, "item/delta", "itemId", "a", "delta", "other"); obj(other.get("params")).put("threadId", "other"); check(!state.apply(other), "Foreign thread");
        check(state.apply(notification(15, "item/completed", "item", item("a", "agentMessage", "Final"))), "Completion replaces text");
        Map<String, Object> late = item("a", "agentMessage", "Final"); late.put("entryId", "saved"); check(state.apply(notification(16, "item/updated", "item", late)), "Late provenance");
        check(state.items.size() == 1 && str(state.items.get("a").get("entryId")).equals("saved"), "Late updated no duplication");
        check(state.apply(notification(14, "item/started", "item", item("b", "reasoning", "thought"))), "Out-of-order independent item");
        check(!state.apply(notification(13, "item/updated", "item", item("a", "agentMessage", "stale"))), "Stale item replacement rejected");
        state.apply(notification(18, "thread/status", "jobs", 2, "timers", 3)); state.apply(notification(17, "thread/status", "jobs", 1, "timers", 1));
        check(num(state.info.get("jobs")) == 2, "Out-of-order status cannot regress");
        check(state.apply(notification(19, "item/delta", "itemId", "missing", "delta", "lost")) && state.needsSnapshot, "Missing delta requests snapshot");
        state.reset(map("threadId", "t", "eventId", 20, "items", List.of(late)));
        check(!state.needsSnapshot && state.items.size() == 1, "Reset replaces all items");
        check(!state.apply(notification(19, "events/reset")), "Old reset ignored after hydration");
        check(state.apply(notification(21, "events/reset")) && state.needsSnapshot, "New reset requests hydration");
        state.reset(map("threadId", "t", "eventId", 0, "serverInstanceId", "two", "items", List.of()));
        check(state.apply(notification(1, "item/started", "item", item("c", "commandExecution", ""))), "New instance cursor");
        state.apply(notification(2, "item/delta", "itemId", "c", "delta", "x".repeat(140000)));
        check(str(state.items.get("c").get("output")).length() == 65536, "Bounded tool stream");
        Map<String, Object> projection = state.projection();
        state.apply(notification(3, "item/delta", "itemId", "c", "delta", "next"));
        check(str(obj(list(projection.get("items")).getFirst()).get("output")).length() == 65536, "Immutable live projection does not change underneath EDT");
        state.apply(notification(4, "prompt/open", "prompt", map("id", "p"))); state.apply(notification(5, "prompt/closed", "id", "different"));
        check(!obj(state.info.get("prompt")).isEmpty(), "Other prompt closure ignored");
        state.apply(notification(6, "prompt/closed", "id", "p")); check(state.info.get("prompt") == null, "Prompt withdrawn");
        Map<String, Object> copied = state.snapshot(); obj(list(copied.get("items")).getFirst()).put("output", "changed"); check(!str(state.items.get("c").get("output")).equals("changed"), "Snapshot is isolated");
    }
    static void markdown() {
        List<Markdown.Block> blocks = Markdown.parse("# Title\n\nhello `code`\n- a\n1. b\n> quote\n```java\na < b\n```\n");
        check(blocks.size() == 6, "Block count"); check(blocks.getFirst().kind().equals("heading") && blocks.getFirst().level() == 1, "Heading");
        check(blocks.getLast().kind().equals("code") && blocks.getLast().language().equals("java"), "Fenced code language");
        check(Markdown.parse("```\npartial").getFirst().text().equals("partial\n"), "Streaming unfinished fence");
        check(Markdown.inline("<script>&").equals("&lt;script&gt;&amp;"), "Raw HTML escaped");
        String inline = Markdown.inline("[ok](https://example.test) [bad](javascript:alert) `x<y` **bold** *italic*");
        check(inline.contains("href=\"https://example.test\"") && !inline.contains("javascript:"), "Safe links only");
        check(inline.contains("<code>x&lt;y</code>") && inline.contains("<b>bold</b>") && inline.contains("<i>italic</i>"), "Inline formatting");
    }
    static final class Fake implements Transport {
        Consumer<String> receive; Consumer<Throwable> lost;
        final BlockingQueue<Map<String, Object>> requests = new LinkedBlockingQueue<>();
        final AtomicInteger connections = new AtomicInteger();
        public void send(String text) {
            Map<String, Object> request = obj(parse(text));
            if ("initialize".equals(request.get("method"))) receive.accept(write(map("id", request.get("id"), "result", map("clientId", "c" + connections.get(), "serverInstanceId", "s"))));
            else if (request.containsKey("id")) requests.add(request);
        }
        public void close() {}
        void reply(Map<String, Object> request, Object value) { receive.accept(write(map("id", request.get("id"), "result", value))); }
    }
    static void protocol() throws Exception {
        Fake fake = new Fake(); Options options = Options.parse(new String[]{});
        Protocol client = new Protocol(options, (o, msg, lost) -> { fake.receive = msg; fake.lost = lost; fake.connections.incrementAndGet(); return fake; });
        BlockingQueue<Map<String, Object>> notifications = new LinkedBlockingQueue<>(); client.notifications = notifications::add;
        try {
            client.connect().get(5, TimeUnit.SECONDS); check(client.ready && client.clientId.equals("c1"), "Handshake");
            CompletableFuture<Object> a = client.call("a", Map.of()), b = client.call("b", Map.of());
            Map<String, Object> ra = fake.requests.poll(3, TimeUnit.SECONDS), rb = fake.requests.poll(3, TimeUnit.SECONDS);
            fake.receive.accept(write(notification(1, "event", "title", "notice")));
            fake.reply(rb, "second"); fake.reply(ra, "first");
            check(a.get(3, TimeUnit.SECONDS).equals("first") && b.get(3, TimeUnit.SECONDS).equals("second"), "IDs match out-of-order replies");
            check(notifications.poll(3, TimeUnit.SECONDS).get("method").equals("event"), "Notifications while requests pending");
            CompletableFuture<Object> bad = client.call("bad", Map.of()); Map<String, Object> request = fake.requests.poll(3, TimeUnit.SECONDS);
            fake.receive.accept(write(map("id", request.get("id"), "error", map("code", -32000, "message", "busy", "data", map("reason", "busy")))));
            try { bad.get(3, TimeUnit.SECONDS); throw new AssertionError("Error not propagated"); } catch (ExecutionException e) { check(e.getCause() instanceof Protocol.RpcError && ((Protocol.RpcError)e.getCause()).reason().equals("busy"), "Structured RPC error"); }
            CompletableFuture<Object> stranded = client.call("accepted-without-reply", Map.of()); fake.requests.poll(3, TimeUnit.SECONDS);
            fake.lost.accept(new EOFException("simulated drop"));
            try { stranded.get(3, TimeUnit.SECONDS); throw new AssertionError("Pending request survived disconnect"); } catch (ExecutionException e) { check(e.getCause() instanceof IOException, "Disconnect fails pending futures"); }
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            while (fake.connections.get() < 2 && System.nanoTime() < deadline) Thread.sleep(20);
            while (!client.ready && System.nanoTime() < deadline) Thread.sleep(20);
            check(fake.connections.get() == 2 && client.ready && client.clientId.equals("c2"), "Reconnect handshakes new client");
            check(fake.requests.isEmpty(), "Accepted input never replayed");
        } finally { client.close(); }
        check(!client.ready, "Close detaches");
    }
    static void core() throws Exception {
        Fake fake = new Fake(); Core core = new Core(Options.parse(new String[]{}), (o, msg, lost) -> { fake.receive = msg; fake.lost = lost; fake.connections.incrementAndGet(); return fake; });
        BlockingQueue<Map<String, Object>> changes = new LinkedBlockingQueue<>(); core.changed = changes::add;
        try {
            core.connect().get(3, TimeUnit.SECONDS);
            var hydrate = core.hydrate("thread/start", Map.of()); Map<String, Object> request = fake.requests.poll(3, TimeUnit.SECONDS);
            fake.receive.accept(write(notification(11, "item/delta", "itemId", "a", "delta", "l")));
            fake.reply(request, map("threadId", "t", "eventId", 10, "items", List.of(item("a", "agentMessage", "He"))));
            fake.receive.accept(write(notification(12, "item/delta", "itemId", "a", "delta", "lo")));
            Map<String, Object> initial = hydrate.get(3, TimeUnit.SECONDS);
            check(str(obj(list(initial.get("items")).getFirst()).get("text")).startsWith("Hel"), "Pre-response delta retained above snapshot cursor");
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(3); boolean hello = false;
            while (!hello && System.nanoTime() < deadline) {
                Map<String, Object> change = changes.poll(100, TimeUnit.MILLISECONDS);
                if (change != null) hello = "Hello".equals(obj(list(change.get("items")).getFirst()).get("text"));
            }
            check(hello, "Events during hydration converge exactly once");
            fake.receive.accept(write(notification(13, "thread/branchChanged")));
            Map<String, Object> reread = fake.requests.poll(3, TimeUnit.SECONDS);
            check("thread/read".equals(reread.get("method")), "Branch movement requests a fresh snapshot");
            fake.reply(reread, map("threadId", "t", "eventId", 13, "items", List.of(item("b", "userMessage", "new branch"))));
            Map<String, Object> last = changes.poll(3, TimeUnit.SECONDS);
            while (last != null && !"b".equals(obj(list(last.get("items")).getFirst()).get("id"))) last = changes.poll(3, TimeUnit.SECONDS);
            check(last != null && list(last.get("items")).size() == 1, "Branch hydration replaces abandoned items");
            var delayed = core.hydrate("thread/read", map("threadId", "t"));
            Map<String, Object> delayedRequest = fake.requests.poll(3, TimeUnit.SECONDS); core.detach("t");
            // Fence the detach before delivering the already-in-flight snapshot.
            core.protocol.dispatch.submit(() -> {}).get(3, TimeUnit.SECONDS);
            fake.reply(delayedRequest, map("threadId", "t", "eventId", 14, "items", List.of()));
            try { delayed.get(3, TimeUnit.SECONDS); throw new AssertionError("Detached hydrate succeeded"); }
            catch (ExecutionException | CancellationException expected) { checks++; }
            check(core.protocol.dispatch.submit(() -> !core.threads.containsKey("t")).get(3, TimeUnit.SECONDS), "Late snapshot cannot reopen detached tab");
        } finally { core.close(); }
    }
    static void preferences() {
        check(Desktop.savedEndpoint("ws://user:secret@localhost:7878/ws?token=secret#fragment").equals("ws://localhost:7878/ws"), "Remembered endpoint strips all credentials/query/fragment");
        check(Desktop.savedEndpoint("unix:///tmp/atto.sock").equals("unix:///tmp/atto.sock"), "Remember Unix endpoint");
        rejects(() -> Options.parse(new String[]{"--connect", "http://localhost"}));
        rejects(() -> Options.parse(new String[]{"--unknown"}));
        check(Options.parse(new String[]{"--atto", "/bin/atto", "--in-process", "--cwd", "/work", "session"}).session().equals("session"), "CLI session and options");
    }
    static void lines() throws Exception {
        PipedInputStream input = new PipedInputStream(); PipedOutputStream feed = new PipedOutputStream(input);
        ByteArrayOutputStream writes = new ByteArrayOutputStream(); BlockingQueue<String> received = new LinkedBlockingQueue<>();
        CountDownLatch lost = new CountDownLatch(1);
        Transport lines = new Transport.Lines(input, writes, feed::close, received::add, e -> lost.countDown());
        try {
            byte[] bytes = "{\"text\":\"😀\"}\n{\"id\":2}\n".getBytes(java.nio.charset.StandardCharsets.UTF_8);
            for (byte b : bytes) feed.write(b); feed.flush();
            check(str(obj(parse(received.poll(3, TimeUnit.SECONDS))).get("text")).equals("😀"), "Fragmented UTF-8 lines");
            check(num(obj(parse(received.poll(3, TimeUnit.SECONDS))).get("id")) == 2, "Multiple lines");
            lines.send("{}"); check(writes.toString(java.nio.charset.StandardCharsets.UTF_8).equals("{}\n"), "Line framing");
            feed.close(); check(lost.await(3, TimeUnit.SECONDS), "EOF signaled");
        } finally { lines.close(); }
    }
}
