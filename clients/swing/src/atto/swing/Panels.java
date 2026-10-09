package atto.swing;

import javax.swing.*;
import javax.swing.tree.*;
import java.awt.*;
import java.awt.event.*;
import java.util.*;
import java.util.List;
import static atto.swing.Json.*;

final class Panels {
    private Panels() {}
    static String pretty(Object value) { StringBuilder text = new StringBuilder(); pretty(value, text, 0); return text.toString(); }
    static void pretty(Object v, StringBuilder out, int indent) {
        if (v instanceof Map<?, ?> map) {
            for (var e : map.entrySet()) { out.append("  ".repeat(indent)).append(e.getKey()).append(": ");
                if (e.getValue() instanceof Map || e.getValue() instanceof List) { out.append('\n'); pretty(e.getValue(), out, indent + 1); }
                else out.append(str(e.getValue())).append('\n'); }
        } else if (v instanceof List<?> list) {
            for (Object entry : list) { out.append("  ".repeat(indent)).append("• "); if (entry instanceof Map || entry instanceof List) { out.append('\n'); pretty(entry, out, indent + 1); } else out.append(str(entry)).append('\n'); }
        } else out.append(str(v));
    }
    static void debug(SessionPane p) {
        JFileChooser chooser = new JFileChooser(); chooser.setFileSelectionMode(JFileChooser.DIRECTORIES_ONLY);
        if (chooser.showSaveDialog(p.desktop.window) != JFileChooser.APPROVE_OPTION) return;
        java.nio.file.Path root = chooser.getSelectedFile().toPath().resolve("atto-debug-" + System.currentTimeMillis());
        p.desktop.core.call("thread/debug", p.id, Map.of()).thenCombine(p.desktop.core.call("thread/debugRequests", p.id, Map.of()), (profiles, requests) -> map("profiles", profiles, "requests", requests)).thenAcceptAsync(value -> {
            try {
                java.nio.file.Files.createDirectories(root); Map<String, Object> profiles = obj(value.get("profiles"));
                java.nio.file.Files.write(root.resolve("heap.pprof"), Base64.getDecoder().decode(str(profiles.get("heap"))));
                java.nio.file.Files.writeString(root.resolve("goroutines.txt"), str(profiles.get("goroutines")));
                java.nio.file.Files.writeString(root.resolve("memstats.json"), write(profiles.get("memory")));
                java.nio.file.Files.writeString(root.resolve("requests.json"), write(value.get("requests")));
                Desktop.edt(() -> p.desktop.text("Debug saved", "Runtime profiles and requests saved to " + root + "\nProfiles are from the server, not the Java UI.", null));
            } catch (Exception e) { throw new java.util.concurrent.CompletionException(e); }
        }).exceptionally(e -> { Desktop.edt(() -> p.desktop.error(e.getMessage())); return null; });
    }
    static void login(SessionPane p, String provider) {
        p.rpc("auth/list", Map.of(), v -> {
            List<Object> available = list(obj(v).get("providers")).stream().filter(x -> provider.isEmpty() || provider.equals(str(obj(x).get("id")))).toList();
            p.desktop.pick("Sign in (credentials stay on server)", available, x -> str(obj(x).get("name")) + " · " + str(obj(x).get("status")), selected -> {
                Map<String, Object> entry = obj(selected);
                if (yes(entry.get("oauth"))) p.rpc("auth/login", map("provider", entry.get("id"), "oauth", true), result -> {});
                else {
                    JPasswordField key = new JPasswordField(45); JDialog dialog = p.desktop.dialog("API key for " + str(entry.get("name")), key);
                    Runnable submit = () -> {
                        char[] secret = key.getPassword(); String text = new String(secret); Arrays.fill(secret, '\0'); key.setText(""); dialog.dispose();
                        p.rpc("auth/login", map("provider", entry.get("id"), "apiKey", text), result -> {});
                    };
                    dialog.add(Desktop.button("Save key on server", submit), BorderLayout.SOUTH); key.addActionListener(e -> submit.run()); dialog.pack(); dialog.setVisible(true);
                }
            });
        });
    }
    static void logout(SessionPane p, String provider) {
        p.rpc("auth/list", Map.of(), v -> p.desktop.pick("Remove saved credentials (environment unchanged)", list(obj(v).get("stored")).stream().filter(x -> provider.isEmpty() || provider.equals(str(x))).toList(), Json::str,
            selected -> p.rpc("auth/logout", map("provider", selected), ignored -> {})));
    }
    static void models(SessionPane p) {
        p.rpc("models/list", Map.of(), v -> p.desktop.pick("Model · provider / context / authentication", list(obj(v).get("models")), entry -> {
            Map<String, Object> m = obj(entry); return str(m.get("id")) + " · " + str(m.get("name")) + " · " + num(m.get("contextWindow")) + " context" + (yes(m.get("hasKey")) ? "" : " · no credentials") + (yes(m.get("images")) ? " · images" : "");
        }, entry -> p.rpc("thread/setModel", map("model", obj(entry).get("id")), ignored -> p.hydrate())));
    }
    static void effort(SessionPane p) {
        List<Object> efforts = list(p.info.get("efforts"));
        if (efforts.isEmpty()) { p.desktop.error("This model exposes no reasoning efforts."); return; }
        p.desktop.pick("Reasoning effort", efforts, Json::str, value -> p.rpc("thread/setEffort", map("effort", value), v -> p.hydrate()));
    }
    record Node(Map<String, Object> entry) {
        public String toString() {
            Map<String, Object> message = obj(entry.get("message")); String text = str(message.get("content"));
            if (text.length() > 110) text = text.substring(0, 110) + "…";
            return str(entry.get("id")) + " · " + str(entry.get("type")) + " " + str(message.get("role")) + " · " + text + " " + str(entry.get("label"));
        }
    }
    static void tree(SessionPane p, boolean fork) {
        p.rpc("thread/tree", Map.of(), v -> {
            Map<String, Object> tree = obj(v); DefaultMutableTreeNode root = new DefaultMutableTreeNode("Session " + p.id + " · leaf " + str(tree.get("leaf")));
            Map<String, DefaultMutableTreeNode> nodes = new LinkedHashMap<>();
            for (Object row : list(tree.get("entries"))) { Map<String, Object> entry = obj(row); nodes.put(str(entry.get("id")), new DefaultMutableTreeNode(new Node(entry))); }
            for (var node : nodes.values()) {
                Node value = (Node)node.getUserObject(); DefaultMutableTreeNode parent = nodes.get(str(value.entry.get("parentId")));
                if (parent != null && parent != node) parent.add(node); else root.add(node);
            }
            JTree view = new JTree(root); view.setRootVisible(true); view.setShowsRootHandles(true);
            for (int i = 0; i < view.getRowCount() && i < 400; i++) view.expandRow(i);
            JScrollPane scroll = new JScrollPane(view); scroll.setPreferredSize(new Dimension(850, 550));
            JDialog dialog = p.desktop.dialog(fork ? "Fork from entry" : "Session tree", scroll);
            JPanel controls = new JPanel(); JComboBox<String> summary = new JComboBox<>(new String[]{"none", "auto", "custom"}); JTextField instructions = new JTextField(25);
            controls.add(new JLabel("Branch summary:")); controls.add(summary); controls.add(instructions);
            Runnable jump = () -> {
                if (!(view.getLastSelectedPathComponent() instanceof DefaultMutableTreeNode selected) || !(selected.getUserObject() instanceof Node node)) return;
                String id = str(node.entry.get("id")); dialog.dispose();
                if (fork) fork(p, id);
                else p.rpc("thread/navigate", map("entryId", id, "summary", map("mode", summary.getSelectedItem(), "instructions", instructions.getText())), x -> {});
            };
            controls.add(Desktop.button(fork ? "Fork" : "Jump", jump));
            controls.add(Desktop.button("Label", () -> {
                if (view.getLastSelectedPathComponent() instanceof DefaultMutableTreeNode selected && selected.getUserObject() instanceof Node node)
                    p.desktop.input("Entry label", "", text -> p.rpc("thread/setLabel", map("entryId", node.entry.get("id"), "label", text), ignored -> {}));
            }));
            Desktop.bind(view, "ENTER", "jump", jump); dialog.add(controls, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true);
        });
    }
    static void fork(SessionPane p, String entry) {
        p.rpc("thread/fork", map("entryId", entry), v -> {
            Map<String, Object> result = obj(v);
            p.desktop.core.hydrate("thread/resume", map("threadId", result.get("threadId"), "deferStart", true)).thenAccept(snapshot -> Desktop.edt(() -> {
                SessionPane forked = p.desktop.sessions.get(str(snapshot.get("threadId"))); if (forked != null) { forked.composer.setText(str(result.get("input"))); forked.images.addAll(list(result.get("images"))); forked.updateAttachments(); }
            }));
        });
    }
    static void goal(SessionPane p) {
        JPanel body = new JPanel(new BorderLayout(8, 8)); JTextArea state = new JTextArea(pretty(p.info.get("goal")), 12, 75); state.setEditable(false); state.setLineWrap(true);
        body.add(new JScrollPane(state), BorderLayout.CENTER); JTextArea objective = new JTextArea(str(obj(p.info.get("goal")).get("objective")), 4, 75); objective.setLineWrap(true);
        body.add(new JScrollPane(objective), BorderLayout.NORTH); JPanel controls = new JPanel();
        JDialog dialog = p.desktop.dialog("Goal", body);
        Runnable refresh = () -> p.rpc("goal/read", Map.of(), v -> state.setText(pretty(obj(v).get("goal"))));
        controls.add(Desktop.button("Set", () -> p.rpc("goal/set", map("input", objective.getText()), v -> refresh.run())));
        controls.add(Desktop.button("Edit", () -> p.rpc("goal/edit", map("input", objective.getText()), v -> refresh.run())));
        for (String method : List.of("pause", "resume", "clear")) controls.add(Desktop.button(method, () -> p.rpc("goal/" + method, Map.of(), v -> refresh.run())));
        controls.add(Desktop.button("Refresh", refresh)); body.add(controls, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true); refresh.run();
    }
    static void jobs(SessionPane p) {
        DefaultListModel<Map<String, Object>> model = new DefaultListModel<>(); JList<Map<String, Object>> list = new JList<>(model);
        list.setSelectionMode(ListSelectionModel.SINGLE_SELECTION);
        list.setCellRenderer(new DefaultListCellRenderer() { public Component getListCellRendererComponent(JList<?> l, Object v, int i, boolean selected, boolean focus) {
            Map<String, Object> job = obj(v); return super.getListCellRendererComponent(l, "#" + num(job.get("id")) + " · " + str(job.get("status")) + " · " + num(job.get("runtimeMs")) / 1000 + "s · " + str(job.get("command")), i, selected, focus);
        } });
        JTextArea output = new JTextArea(22, 80); output.setEditable(false); output.setFont(new Font(Font.MONOSPACED, Font.PLAIN, p.desktop.fontSize));
        JCheckBox follow = new JCheckBox("Follow selected job", true);
        JPanel body = new JPanel(new BorderLayout()); body.add(new JScrollPane(list), BorderLayout.NORTH); body.add(new JScrollPane(output), BorderLayout.CENTER);
        JDialog dialog = p.desktop.dialog("Background jobs", body);
        Runnable refresh = () -> p.rpc("job/list", Map.of(), v -> {
            long selected = list.getSelectedValue() == null ? -1 : num(list.getSelectedValue().get("id")); model.clear();
            for (Object row : Json.list(obj(v).get("jobs"))) model.addElement(obj(row));
            for (int i = 0; i < model.size(); i++) if (num(model.get(i).get("id")) == selected) list.setSelectedIndex(i);
        });
        Runnable read = () -> {
            if (list.getSelectedValue() != null) {
                Object job = list.getSelectedValue().get("id"); p.rpc("job/output", map("job", job, "lines", 2000), v -> {
                    if (list.getSelectedValue() != null && job.equals(list.getSelectedValue().get("id"))) { output.setText(str(obj(v).get("output"))); if (follow.isSelected()) output.setCaretPosition(output.getDocument().getLength()); }
                });
            }
        };
        list.addListSelectionListener(e -> { if (!e.getValueIsAdjusting()) read.run(); });
        JPanel controls = new JPanel(); controls.add(follow); controls.add(Desktop.button("Refresh", refresh)); controls.add(Desktop.button("Copy output", () -> Desktop.copy(output.getText())));
        controls.add(Desktop.button("Stop selected", () -> { if (list.getSelectedValue() != null) p.rpc("job/stop", map("job", list.getSelectedValue().get("id")), v -> refresh.run()); }));
        controls.add(Desktop.button("Stop all", () -> p.rpc("job/stopAll", Map.of(), v -> refresh.run()))); body.add(controls, BorderLayout.SOUTH);
        javax.swing.Timer timer = new javax.swing.Timer(1500, e -> { refresh.run(); if (follow.isSelected()) read.run(); }); timer.start();
        dialog.addWindowListener(new WindowAdapter() { public void windowClosed(WindowEvent e) { timer.stop(); } }); dialog.pack(); dialog.setVisible(true); refresh.run();
    }
    static void timers(SessionPane p) {
        p.rpc("timer/list", Map.of(), v -> p.desktop.pick("Timers · select to cancel", list(obj(v).get("timers")), Panels::pretty, timer -> p.rpc("timer/cancel", map("id", obj(timer).get("id")), x -> {})));
    }
    static void agents(SessionPane p) {
        p.rpc("agent/list", Map.of(), v -> p.desktop.pick("Agents (read-only)", list(obj(v).get("agents")), entry -> {
            Map<String, Object> a = obj(entry); return str(a.get("name")) + " · " + str(a.get("status")) + " · " + str(a.get("model")) + " · " + str(a.get("task"));
        }, agent -> p.rpc("agent/read", map("name", obj(agent).get("name")), transcript -> p.desktop.text("Agent transcript (read-only)", pretty(transcript), null))));
    }
}
