package atto.swing;

import java.util.*;
import java.util.concurrent.*;
import static atto.swing.Json.*;

/** Headless live-protocol smoke test. The Go test supplies a scripted provider. */
final class SelfTest {
    private SelfTest() {}
    static Object call(Core core, String method, String id, Map<String, Object> fields) throws Exception { return core.call(method, id, fields).get(30, TimeUnit.SECONDS); }
    static void check(boolean ok, String message) { if (!ok) throw new AssertionError(message); }
    static Map<String, Object> read(Core core, String id) throws Exception { return core.hydrate("thread/read", map("threadId", id)).get(30, TimeUnit.SECONDS); }
    static Map<String, Object> idle(Core core, String id) throws Exception {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(30);
        while (System.nanoTime() < deadline) { Map<String, Object> state = read(core, id); if (!yes(state.get("busy")) && list(state.get("items")).stream().noneMatch(x -> yes(obj(x).get("shell")) && "inProgress".equals(obj(x).get("status")))) return state; Thread.sleep(30); }
        throw new AssertionError("Thread did not become idle");
    }
    static void run(Options options) throws Exception {
        try (Core core = new Core(options)) {
            core.error = e -> System.err.println("core: " + e);
            BlockingQueue<String> connections = new LinkedBlockingQueue<>(); core.connection = connections::add;
            BlockingQueue<Map<String, Object>> events = new LinkedBlockingQueue<>(); core.event = events::add;
            core.connect().get(30, TimeUnit.SECONDS);
            Map<String, Object> models = obj(call(core, "models/list", "", Map.of()));
            check(list(models.get("models")).stream().anyMatch(x -> "fake/m".equals(obj(x).get("id"))), "--selftest requires the providertest fake/m fixture; run go test ./clients/swing instead of a live provider");
            List<Object> defaultRows = list(obj(call(core, "thread/list", "", Map.of())).get("threads"));
            check(defaultRows.stream().noneMatch(x -> !obj(obj(x).get("agent")).isEmpty()), "Default inventory leaked agent sessions");
            List<Object> inventory = list(obj(call(core, "thread/list", "", SessionInventory.options())).get("threads"));
            List<Map<String, Object>> inventoryRows = SessionInventory.tree(inventory, "");
            int inventoryAgents = 0;
            for (Map<String, Object> row : inventoryRows) if (!obj(row.get("agent")).isEmpty()) {
                inventoryAgents++;
                Map<String, Object> agent = obj(row.get("agent"));
                check(str(row.get("preview")).equals("Inventory first task") && str(row.get("lastMessage")).equals("Inventory last report"), "Inventory task/report missing");
                check(str(row.get("branch")).equals("atto/inventory") && str(agent.get("role")).equals("review") && str(obj(agent.get("spawnedBy")).get("toolCallId")).equals("inventory-call"), "Inventory metadata missing");
                check(num(row.get("displayDepth")) == 1 && !str(agent.get("parentThreadId")).isEmpty() && !yes(row.get("loaded")), "Listing attached agent or lost ancestry");
                if (str(agent.get("name")).equals("closed-agent")) check(yes(row.get("archived")) && str(agent.get("lifecycle")).equals("closed"), "Closed archive missing");
            }
            check(inventoryAgents == 2, "Agent inventory fixtures missing");
            Map<String, Object> thread = core.hydrate("thread/start", map("cwd", options.cwd(), "model", "fake/m")).get(30, TimeUnit.SECONDS);
            String id = str(thread.get("threadId")); check(!id.isEmpty(), "Missing thread id");
            call(core, "input/submit", id, map("input", "hello swing", "intent", "auto"));
            // The scripted first response streams slowly, leaving time for pending input.
            call(core, "input/submit", id, map("input", "steer swing", "intent", "steer"));
            Object queued = call(core, "input/submit", id, map("input", "queued swing", "intent", "queue"));
            Object taken = call(core, "turn/unsteer", id, map("inputId", obj(queued).get("inputId"))); check(str(obj(taken).get("text")).equals("queued swing"), "Queue takeback");
            Map<String, Object> state = idle(core, id);
            check(list(state.get("items")).stream().anyMatch(x -> "agentMessage".equals(obj(x).get("type")) && str(obj(x).get("text")).contains("Swing answer")), "Missing streamed text");
            long deltaCount = events.stream().filter(n -> "item/delta".equals(n.get("method"))).count(); check(deltaCount >= 2, "No streaming deltas");
            call(core, "input/submit", id, map("input", "/name Swing test"));
            call(core, "thread/setModel", id, map("model", thread.get("model")));
            check(str(read(core, id).get("name")).equals("Swing test"), "Slash name did not persist");
            // A mirrored prompt exercises the native first-answer-wins lifecycle.
            Map<String, Object> prompt = obj(call(core, "prompt/clientOpen", id, map("prompt", map("requestId", "selftest-prompt", "kind", "input", "title", "Self-test prompt"))));
            check(!str(prompt.get("id")).isEmpty(), "Prompt did not open");
            Map<String, Object> waiting = list(obj(call(core, "thread/list", "", SessionInventory.options())).get("threads")).stream().map(Json::obj).filter(x -> id.equals(str(x.get("threadId")))).findFirst().orElseThrow();
            check(yes(waiting.get("openPrompt")) && SessionInventory.status(waiting).equals("Needs you"), "Inventory prompt attention flag missing");
            call(core, "prompt/answer", id, map("id", prompt.get("id"), "text", "answer"));
            if (!System.getProperty("os.name").startsWith("Windows")) {
                events.clear();
                call(core, "input/submit", id, map("input", "run hosted command"));
                long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(20);
                boolean started = false;
                while (System.nanoTime() < deadline) {
                    Map<String, Object> n = events.poll(1, TimeUnit.SECONDS);
                    if (n != null && "item/delta".equals(n.get("method")) && str(obj(n.get("params")).get("delta")).contains("hosted-start")) { started = true; break; }
                }
                check(started, "Hosted command did not stream");
                call(core, "turn/interrupt", id, map("mode", "cancel"));
                Map<String, Object> interrupted = idle(core, id);
                Map<String, Object> command = list(interrupted.get("items")).stream().map(Json::obj).filter(x -> num(x.get("job")) > 0).findFirst().orElseThrow();
                check("interrupt".equals(command.get("background")) && !yes(command.get("canceled")), "Interrupt killed command instead of backgrounding");
                Map<String, Object> jobs = obj(call(core, "job/list", id, Map.of()));
                check(!list(jobs.get("jobs")).isEmpty(), "Background job missing");
                Object output = call(core, "job/output", id, map("job", command.get("job"), "lines", 200));
                check(str(obj(output).get("output")).contains("hosted-start"), "Background job output missing");
            }
            if (!System.getProperty("os.name").startsWith("Windows")) {
            call(core, "input/submit", id, map("input", "execute queued follow-up", "intent", "auto"));
            call(core, "input/submit", id, map("input", "queued executed", "intent", "queue"));
            Map<String, Object> queuedState = idle(core, id);
            check(list(queuedState.get("items")).stream().anyMatch(x -> "queued executed".equals(obj(x).get("text"))), "Queued follow-up did not execute");
            }
            // User shells use the same host as ! commands.
            call(core, "shell/start", id, map("command", System.getProperty("os.name").startsWith("Windows") ? "echo shell-start" : "echo shell-start; sleep 1; echo shell-end"));
            idle(core, id);
            String entry = list(read(core, id).get("items")).stream().map(Json::obj).filter(x -> "userMessage".equals(x.get("type")) && !str(x.get("entryId")).isEmpty()).map(x -> str(x.get("entryId"))).findFirst().orElseThrow();
            Map<String, Object> fork = obj(call(core, "thread/fork", id, map("entryId", entry)));
            check(!str(fork.get("path")).isEmpty(), "Fork path missing");
            Map<String, Object> forked = core.hydrate("thread/resume", map("threadId", fork.get("threadId"))).get(30, TimeUnit.SECONDS);
            check(!str(forked.get("threadId")).equals(id), "Fork reused session id");
            call(core, "thread/close", str(forked.get("threadId")), Map.of());
            call(core, "thread/detach", id, Map.of());
            Map<String, Object> reattached = core.hydrate("thread/resume", map("threadId", id)).get(30, TimeUnit.SECONDS);
            check(list(reattached.get("items")).stream().map(x -> str(obj(x).get("id"))).distinct().count() == list(reattached.get("items")).size(), "Duplicate transcript after resume");
            connections.clear(); core.protocol.reconnect();
            long reconnectDeadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(15); boolean connected = false;
            while (System.nanoTime() < reconnectDeadline && !connected) connected = "Connected".equals(connections.poll(1, TimeUnit.SECONDS));
            check(connected, "Transport did not reconnect");
            Map<String, Object> recovered = core.hydrate("thread/resume", map("threadId", id)).get(30, TimeUnit.SECONDS);
            check(list(recovered.get("items")).stream().map(x -> str(obj(x).get("id"))).distinct().count() == list(recovered.get("items")).size(), "Duplicates after reconnect snapshot");
            if (!System.getProperty("os.name").startsWith("Windows")) check(list(recovered.get("items")).stream().anyMatch(x -> "queued executed".equals(obj(x).get("text"))), "Reconnect lost saved queued input");
            int pagingItems = 0;
            if (!options.session().isEmpty()) {
                Map<String, Object> paged = core.hydrate("thread/resume", map("threadId", options.session(), "deferStart", true)).get(30, TimeUnit.SECONDS);
                String pagingId = str(paged.get("threadId"));
                check(yes(paged.get("hasMore")) && list(paged.get("items")).size() <= 200, "Large-session attach was not paged");
                check(list(paged.get("items")).stream().noneMatch(x -> str(obj(x).get("text")).startsWith("paging-000")), "Attach included oldest message");
                while (yes(paged.get("hasMore"))) {
                    core.loadEarlier(pagingId).get(30, TimeUnit.SECONDS);
                    paged = core.protocol.dispatch.submit(() -> core.threads.get(pagingId).snapshot()).get(30, TimeUnit.SECONDS);
                }
                pagingItems = (int)list(paged.get("items")).stream().filter(x -> str(obj(x).get("text")).startsWith("paging-")).count();
                check(pagingItems == 350, "Earlier paging lost transcript items");
                check(list(paged.get("items")).stream().map(x -> str(obj(x).get("id"))).distinct().count() == list(paged.get("items")).size(), "Pages duplicated items");
                call(core, "thread/close", pagingId, Map.of());
            }
            System.out.println(write(map("selftest", "passed", "threadId", id, "sessionPath", reattached.get("sessionPath"), "items", list(reattached.get("items")).size(), "pagingItems", pagingItems, "inventoryAgents", inventoryAgents)));
            call(core, "thread/close", id, Map.of());
        }
    }
}
