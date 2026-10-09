package atto.swing;

import java.util.*;
import static atto.swing.Json.*;

/** Like server.ThreadView: snapshots are the cursor boundary, not an append. */
public final class ThreadState {
    public Map<String, Object> info = new LinkedHashMap<>();
    public final LinkedHashMap<String, Map<String, Object>> items = new LinkedHashMap<>();
    public long cursor;
 public long generation;
    public boolean needsSnapshot;
    private long snapshotCursor;
    private final Set<Long> seen = new HashSet<>();
    private final Map<String, Long> itemVersions = new HashMap<>();
    private final Map<String, Long> fieldVersions = new HashMap<>();

    public void reset(Map<String, Object> snapshot) {
        generation++;
 info = new LinkedHashMap<>(obj(freeze(snapshot)));
        items.clear(); seen.clear(); itemVersions.clear(); fieldVersions.clear();
        for (Object it : list(info.remove("items"))) {
            Map<String, Object> item = obj(freeze(it)); items.put(str(item.get("id")), item);
        }
        cursor = snapshotCursor = num(info.get("eventId")); needsSnapshot = false;
    }
    /** Earlier pages do not move the live snapshot/event boundary. */
    public void prepend(Map<String, Object> page) {
        LinkedHashMap<String, Map<String, Object>> merged = new LinkedHashMap<>();
        for (Object value : list(page.get("items"))) {
            Map<String, Object> item = obj(freeze(value)); String id = str(item.get("id"));
            if (!items.containsKey(id)) merged.put(id, item);
        }
        merged.putAll(items); items.clear(); items.putAll(merged);
        info.put("hasMore", yes(page.get("hasMore"))); info.put("before", page.get("before"));
    }
    public boolean apply(Map<String, Object> event) {
        String method = str(event.get("method")); Map<String, Object> p = obj(event.get("params"));
        String id = str(p.get("threadId"));
        if (!id.isEmpty() && !id.equals(str(info.get("threadId")))) return false;
        long eventId = num(event.get("eventId"));
        if (method.equals("events/reset")) {
            if (eventId != 0 && eventId <= snapshotCursor) return false;
            needsSnapshot = true; return true;
        }
        if (needsSnapshot || (eventId != 0 && (eventId <= snapshotCursor || !seen.add(eventId)))) return false;
        cursor = Math.max(cursor, eventId);
        switch (method) {
            case "item/started", "item/updated", "item/completed" -> {
                Map<String, Object> item = new LinkedHashMap<>(obj(copy(p.get("item"))));
                String itemId = str(item.get("id")); if (itemId.isEmpty()) return false;
                if (eventId != 0 && eventId < itemVersions.getOrDefault(itemId, 0L)) return false;
                items.put(itemId, obj(freeze(item))); itemVersions.put(itemId, eventId);
            }
            case "item/delta" -> {
                String itemId = str(p.get("itemId")); Map<String, Object> item = items.get(itemId);
                if (item == null || (eventId != 0 && eventId < itemVersions.getOrDefault(itemId, 0L))) {
                    // A missing/out-of-order delta cannot safely be appended. Rehydrate.
                    needsSnapshot = true; return true;
                }
                item = new LinkedHashMap<>(item);
                String field = "commandExecution".equals(item.get("type")) ? "output" : "text";
                String text = str(item.get(field)) + str(p.get("delta"));
                if (field.equals("output") && text.length() > 128 * 1024) {
                    int cut = text.length() - 64 * 1024;
                    item.put("dropped", num(item.get("dropped")) + cut); text = text.substring(cut);
                }
                item.put(field, text); items.put(itemId, Collections.unmodifiableMap(item)); itemVersions.put(itemId, eventId);
            }
            case "item/display" -> {
                Map<String, Object> item = items.get(str(p.get("itemId")));
                if (item != null) {
                    Map<String, Object> next = new LinkedHashMap<>(item); next.put("display", freeze(p.get("display")));
                    items.put(str(p.get("itemId")), obj(freeze(next)));
                }
            }
            case "thread/updated" -> {
                Map<String, Object> thread = new LinkedHashMap<>(obj(copy(p.get("thread")))); thread.remove("items");
                for (var e : thread.entrySet()) set(e.getKey(), e.getValue(), eventId);
            }
            case "turn/started" -> {
                set("busy", true, eventId); set("turnId", p.get("turnId"), eventId); set("runKind", p.get("runKind"), eventId);
                set("turn", map("startedAt", p.get("startedAt"), "verb", p.get("verb")), eventId);
                set("activity", map("phase", p.get("activity"), "startedAt", p.get("startedAt")), eventId);
            }
            case "turn/completed" -> {
                set("busy", false, eventId); set("turnId", "", eventId); set("turn", null, eventId); set("activity", null, eventId);
                if (p.containsKey("usage")) set("usage", p.get("usage"), eventId);
                if (p.containsKey("contextTokens")) set("contextTokens", p.get("contextTokens"), eventId);
            }
            case "turn/activity" -> set("activity", p.get("activity"), eventId);
            case "turn/pending" -> set("pending", p.get("pending"), eventId);
            case "thread/usage" -> { set("usage", p.get("usage"), eventId); set("contextTokens", p.get("contextTokens"), eventId); }
            case "thread/status" -> { set("jobs", p.get("jobs"), eventId); set("timers", p.get("timers"), eventId); }
            case "goal/updated" -> set("goal", p.get("goal"), eventId);
            case "extension/ui" -> set("extensionUi", p.get("ui"), eventId);
            case "thread/reloaded" -> { if (p.containsKey("context")) set("context", p.get("context"), eventId); }
            case "prompt/open" -> set("prompt", p.get("prompt"), eventId);
            case "prompt/closed" -> {
                if (str(obj(info.get("prompt")).get("id")).equals(str(p.get("id")))) set("prompt", null, eventId);
            }
            case "thread/branchChanged", "thread/switched" -> needsSnapshot = true;
            case "thread/closed" -> { set("closed", true, eventId); set("busy", false, eventId); }
            default -> { return false; }
        }
        if (seen.size() > 10000) needsSnapshot = true;
        return true;
    }
    private void set(String field, Object value, long version) {
        if (version == 0 || version >= fieldVersions.getOrDefault(field, 0L)) {
            info.put(field, freeze(value)); fieldVersions.put(field, version);
        }
    }
    Map<String, Object> projection() {
        Map<String, Object> result = new LinkedHashMap<>(info);
        result.put("items", Collections.unmodifiableList(new ArrayList<>(items.values()))); result.put("eventId", cursor);
        return Collections.unmodifiableMap(result);
    }
    public Map<String, Object> snapshot() {
        Map<String, Object> result = new LinkedHashMap<>(info);
        result.put("items", new ArrayList<>(items.values())); result.put("eventId", cursor);
        return obj(copy(result));
    }
}
