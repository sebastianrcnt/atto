package atto.swing;

import java.util.*;
import static atto.swing.Json.*;

/** Presentation of thread/list; never consults local files or worker discovery. */
final class SessionInventory {
    static Map<String, Object> options() { return map("includeAgents", true, "includeClosedAgents", true, "includeArchived", true); }
    static String parent(Map<String, Object> row) { return str(obj(row.get("agent")).get("parentThreadId")); }
    static String title(Map<String, Object> row) {
        Map<String, Object> agent = obj(row.get("agent"));
        String path = str(agent.get("path"));
        if (path.startsWith("/root/") && !parent(row).isEmpty()) return path.substring(6);
        String name = str(agent.get("name")); return name.isEmpty() ? Desktop.sessionName(row) : name;
    }
    static String status(Map<String, Object> row) {
        if (yes(row.get("openPrompt")) || yes(row.get("goalWaiting"))) return "Needs you";
        Map<String, Object> agent = obj(row.get("agent")); String turn = str(obj(agent.get("lastTurn")).get("status"));
        if (yes(row.get("busy")) || turn.equals("running") || turn.equals("queued")) return "Working";
        if (yes(row.get("archived")) || "closed".equals(agent.get("lifecycle"))) return "Inactive";
        if (yes(row.get("loaded")) || turn.equals("idle") || turn.equals("done")) return "Ready";
        return "Inactive";
    }
    static String details(Map<String, Object> row) {
        Map<String, Object> agent = obj(row.get("agent")), turn = obj(agent.get("lastTurn")), by = obj(agent.get("spawnedBy"));
        String text = str(row.get("preview")) + "\n\n" + str(row.get("lastMessage"));
        text += "\n\n" + status(row) + " · " + str(row.get("model")) + "\n" + str(row.get("branch")) + "\n" + str(row.get("cwd"));
        if (!agent.isEmpty()) text += "\n" + str(agent.get("path")) + " · " + str(agent.get("role")) + "\n" + str(agent.get("origin")) + " · " + str(agent.get("project")) + "\nStarted by: " + write(by) + "\nTokens: " + num(turn.get("promptTokens")) + " in · " + num(turn.get("cachedTokens")) + " cached · " + num(turn.get("outputTokens")) + " out\nDuration: " + num(agent.get("durationMs")) + "ms";
        return text;
    }
    static List<Map<String, Object>> tree(List<Object> rows, String query) {
        Map<String, Map<String, Object>> byId = new LinkedHashMap<>();
        for (Object value : rows) { Map<String, Object> row = obj(value); byId.put(str(row.get("threadId")), row); }
        Set<String> include = new HashSet<>();
        for (var entry : byId.entrySet()) if (write(entry.getValue()).toLowerCase(Locale.ROOT).contains(query.toLowerCase(Locale.ROOT))) {
            String id = entry.getKey(); while (!id.isEmpty() && include.add(id)) { Map<String, Object> row = byId.get(id); if (row == null) break; id = parent(row); }
        }
        Map<String, List<String>> children = new LinkedHashMap<>();
        for (var entry : byId.entrySet()) children.computeIfAbsent(parent(entry.getValue()), k -> new ArrayList<>()).add(entry.getKey());
        List<Map<String, Object>> out = new ArrayList<>(); Set<String> seen = new HashSet<>();
        for (var entry : byId.entrySet()) if (!byId.containsKey(parent(entry.getValue()))) walk(entry.getKey(), 0, project(entry.getValue()), byId, children, include, seen, out);
        for (var entry : byId.entrySet()) if (!seen.contains(entry.getKey())) walk(entry.getKey(), 0, project(entry.getValue()), byId, children, include, seen, out);
        return out;
    }
    private static String project(Map<String, Object> row) {
        Map<String, Object> agent = obj(row.get("agent")); String project = str(agent.get("project"));
        return parent(row).isEmpty() && !project.isEmpty() ? project : str(row.get("cwd"));
    }
    private static void walk(String id, int depth, String project, Map<String, Map<String, Object>> byId, Map<String, List<String>> children, Set<String> include, Set<String> seen, List<Map<String, Object>> out) {
        if (!seen.add(id)) return;
        if (include.contains(id)) { Map<String, Object> row = new LinkedHashMap<>(byId.get(id)); row.put("displayDepth", depth); row.put("displayProject", project); out.add(row); }
        for (String child : children.getOrDefault(id, List.of())) walk(child, depth + 1, project, byId, children, include, seen, out);
    }
}
