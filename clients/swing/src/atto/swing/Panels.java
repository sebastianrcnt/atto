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
                Desktop.edt(() -> p.desktop.notice("Debug export saved", "Saved heap profile, goroutines, memory statistics and request history.\n" + Ui.cwd(root.toString()) + "\nProfiles are from the server, not the Java UI."));
            } catch (Exception e) { throw new java.util.concurrent.CompletionException(e); }
        }).exceptionally(e -> { Desktop.edt(() -> p.desktop.error(e.getMessage())); return null; });
    }
    static void usage(SessionPane p) {
        Map<String, Object> usage = obj(p.info.get("usage"));
        String text = "Input tokens: " + num(usage.get("inputTokens")) + "\nCached input: " + num(usage.get("cachedInputTokens")) + "\nOutput tokens: " + num(usage.get("outputTokens")) + "\nEstimated cost: $" + str(usage.getOrDefault("cost", 0));
        p.desktop.notice("Session usage", text);
    }
    static void context(SessionPane p, boolean system) {
        p.rpc("thread/context", system ? map("view", "system") : Map.of(), value -> {
            Map<String, Object> info = obj(value);
            if (system) { p.desktop.text("System prompt", str(info.get("systemPrompt")), null); return; }
            JPanel body = new JPanel(new BorderLayout(0, 16)); body.setPreferredSize(new Dimension(600, 420));
            JLabel title = new JLabel("Context · " + SessionPane.compact(num(info.get("contextTokens"))) + " / " + SessionPane.compact(num(info.get("contextWindow"))) + " tokens"); title.setFont(Ui.body(18).deriveFont(Font.BOLD)); body.add(title, BorderLayout.NORTH);
            JPanel rows = new JPanel(new GridLayout(0, 2, 12, 8)); Map<String, Object> breakdown = obj(info.get("breakdown"));
            for (String field : List.of("System", "Tools", "User", "Images", "Notes", "Assistant", "Reasoning", "ToolCalls", "ToolResults")) {
                rows.add(new JLabel(field.replaceAll("([a-z])([A-Z])", "$1 $2"))); rows.add(Ui.muted(SessionPane.compact(num(breakdown.get(field))) + " characters"));
            }
            body.add(rows); body.add(Ui.muted(yes(info.get("busy")) ? "Breakdown will be available when the turn finishes." : "Sizes are characters; token usage above is the runtime's estimate."), BorderLayout.SOUTH); p.desktop.showDialog(p.desktop.dialog("Context", body));
        });
    }
    static void authentication(SessionPane p) {
        p.rpc("auth/list", Map.of(), value -> {
            JPanel body = new JPanel(); body.setLayout(new BoxLayout(body, BoxLayout.Y_AXIS));
            JLabel title = new JLabel("Provider authentication"); title.setFont(Ui.body(18).deriveFont(Font.BOLD)); body.add(title); body.add(Box.createVerticalStrut(16));
            for (Object valueProvider : list(obj(value).get("providers"))) { Map<String, Object> provider = obj(valueProvider); JPanel row = new JPanel(new BorderLayout(12, 0)); row.setBorder(BorderFactory.createEmptyBorder(8, 0, 8, 0));
                row.add(new JLabel(str(provider.get("name")) + " · " + str(provider.get("status")))); row.add(Desktop.button("Sign in", () -> login(p, str(provider.get("id")))), BorderLayout.EAST); body.add(row);
            }
            body.add(Box.createVerticalStrut(12)); body.add(Ui.muted("Credentials remain on the server. Environment credentials are not removed by sign out.")); p.desktop.showDialog(p.desktop.dialog("Authentication", body));
        });
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
                    dialog.add(Desktop.button("Save key on server", submit), BorderLayout.SOUTH); key.addActionListener(e -> submit.run()); p.desktop.showDialog(dialog);
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
            Map<String, Object> m = obj(entry); return str(m.get("name")) + " · " + str(m.get("id")).split("/", 2)[0] + " · " + num(m.get("contextWindow")) + " context" + (yes(m.get("hasKey")) ? "" : " · no credentials") + (yes(m.get("images")) ? " · images" : "");
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
            String role = str(message.get("role")); String label = str(entry.get("label"));
            return (role.isEmpty() ? str(entry.get("type")) : role.equals("user") ? "You" : "atto") + (text.isEmpty() ? "" : " · " + text) + (label.isEmpty() ? "" : "  [" + label + "]");
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
            Desktop.bind(view, "ENTER", "jump", jump); dialog.add(controls, BorderLayout.SOUTH); p.desktop.showDialog(dialog);
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
    static String goalSummary(Map<String, Object> goal) {
        if (goal.isEmpty()) return "No active goal. Set an objective to let atto continue working between turns.";
        String status = str(goal.getOrDefault("statusLabel", goal.getOrDefault("status", "")));
        return status + "\n\n" + num(obj(goal.get("state")).get("turns")) + " turns · " + num(goal.get("tokensUsed")) + " tokens\n" + str(goal.getOrDefault("summary", "")) + "\n\n" + str(goal.getOrDefault("note", ""));
    }
    static void goal(SessionPane p) {
        JPanel body = new JPanel(new BorderLayout(0, 16)); body.setPreferredSize(new Dimension(660, 350));
        JTextArea objective = new JTextArea(str(obj(p.info.get("goal")).get("objective")), 4, 55); objective.setLineWrap(true); objective.setWrapStyleWord(true); objective.setFont(Ui.body(16)); objective.setBorder(BorderFactory.createEmptyBorder(12, 12, 12, 12));
        JPanel heading = new JPanel(new BorderLayout(0, 8)); JLabel title = new JLabel("What should atto achieve?"); title.setFont(Ui.body(18).deriveFont(Font.BOLD)); heading.add(title, BorderLayout.NORTH); heading.add(new JScrollPane(objective)); body.add(heading, BorderLayout.NORTH);
        JTextArea state = new JTextArea(goalSummary(obj(p.info.get("goal")))); state.setEditable(false); state.setLineWrap(true); state.setWrapStyleWord(true); state.setFont(Ui.body(14)); state.setForeground(Ui.muted); body.add(state);
        JPanel controls = new JPanel(new FlowLayout(FlowLayout.RIGHT, 8, 0)); JDialog dialog = p.desktop.dialog("Goal", body);
        Runnable refresh = () -> p.rpc("goal/read", Map.of(), v -> { Map<String, Object> goal = obj(obj(v).get("goal")); state.setText(goalSummary(goal)); if (objective.getText().isEmpty()) objective.setText(str(goal.get("objective"))); });
        controls.add(Desktop.button("Clear", () -> p.rpc("goal/clear", Map.of(), v -> refresh.run())));
        controls.add(Desktop.button("Pause", () -> p.rpc("goal/pause", Map.of(), v -> refresh.run())));
        controls.add(Desktop.button("Resume", () -> p.rpc("goal/resume", Map.of(), v -> refresh.run())));
        JButton set = Desktop.button("Set goal", () -> p.rpc("goal/set", map("input", objective.getText()), v -> refresh.run())); set.putClientProperty("primary", true); controls.add(set);
        body.add(controls, BorderLayout.SOUTH); javax.swing.Timer timer = new javax.swing.Timer(1500, e -> refresh.run()); timer.start(); dialog.addWindowListener(new WindowAdapter() { public void windowClosed(WindowEvent e) { timer.stop(); } }); p.desktop.showDialog(dialog); refresh.run();
    }
    static void jobs(SessionPane p) {
        DefaultListModel<Map<String, Object>> model = new DefaultListModel<>(); JList<Map<String, Object>> list = new JList<>(model);
        list.setSelectionMode(ListSelectionModel.SINGLE_SELECTION);
        list.setCellRenderer(new DefaultListCellRenderer() { public Component getListCellRendererComponent(JList<?> l, Object v, int i, boolean selected, boolean focus) {
            Map<String, Object> job = obj(v); return super.getListCellRendererComponent(l, "<html><b>" + Markdown.escape(str(job.get("command"))) + "</b><br><span style='color:" + Transcript.color(Ui.muted) + "'>Job #" + num(job.get("id")) + " · " + str(job.get("status")) + " · " + num(job.get("runtimeMs")) / 1000 + "s</span></html>", i, selected, focus);
        } });
        JTextArea output = new JTextArea(22, 80); output.setEditable(false); output.setFont(new Font(Font.MONOSPACED, Font.PLAIN, Math.max(12, p.desktop.fontSize - 1))); output.setBorder(BorderFactory.createEmptyBorder(12, 12, 12, 12));
        JCheckBox follow = new JCheckBox("Follow selected job", true);
        JPanel body = new JPanel(new BorderLayout(0, 12)); body.setPreferredSize(new Dimension(820, 450)); JScrollPane jobList = new JScrollPane(list); jobList.setPreferredSize(new Dimension(820, 110)); body.add(jobList, BorderLayout.NORTH); body.add(new JScrollPane(output), BorderLayout.CENTER);
        JDialog dialog = p.desktop.dialog("Background jobs", body);
        Runnable refresh = () -> p.rpc("job/list", Map.of(), v -> {
            long selected = list.getSelectedValue() == null ? -1 : num(list.getSelectedValue().get("id")); model.clear();
            for (Object row : Json.list(obj(v).get("jobs"))) model.addElement(obj(row));
            for (int i = 0; i < model.size(); i++) if (num(model.get(i).get("id")) == selected) list.setSelectedIndex(i);
            if (list.getSelectedIndex() < 0 && !model.isEmpty()) list.setSelectedIndex(0);
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
        dialog.addWindowListener(new WindowAdapter() { public void windowClosed(WindowEvent e) { timer.stop(); } }); p.desktop.showDialog(dialog); refresh.run();
    }
    static void timers(SessionPane p) {
        p.rpc("timer/list", Map.of(), v -> p.desktop.pick("Timers · select to cancel", list(obj(v).get("timers")), timer -> str(obj(timer).get("message")) + " · " + str(obj(timer).get("when")), timer -> p.rpc("timer/cancel", map("id", obj(timer).get("id")), x -> {})));
    }
    static void agents(SessionPane p) {
        p.rpc("thread/list", SessionInventory.options(), value -> {
            List<Map<String, Object>> rows = SessionInventory.tree(list(obj(value).get("threads")), "");
            DefaultMutableTreeNode root = new DefaultMutableTreeNode("Agent team · read only");
            Map<String, DefaultMutableTreeNode> nodes = new LinkedHashMap<>(); Map<String, Map<String, Object>> entries = new LinkedHashMap<>();
            // Locate the selected session's root by parent links, not a disk tree.
            for (Map<String, Object> row : rows) entries.put(str(row.get("threadId")), row);
            String rootId = p.id; Set<String> seen = new HashSet<>();
            while (seen.add(rootId) && entries.containsKey(rootId) && entries.containsKey(SessionInventory.parent(entries.get(rootId)))) rootId = SessionInventory.parent(entries.get(rootId));
            Set<String> team = new HashSet<>(); team.add(rootId);
            for (Map<String, Object> row : rows) {
                String id = str(row.get("threadId"));
                if (!team.contains(id) && !team.contains(SessionInventory.parent(row))) continue;
                team.add(id); DefaultMutableTreeNode node = new DefaultMutableTreeNode(SessionInventory.title(row) + " · " + SessionInventory.status(row));
                DefaultMutableTreeNode parent = nodes.get(SessionInventory.parent(row)); (parent == null ? root : parent).add(node); nodes.put(id, node);
            }
            JTree view = new JTree(root); view.setRowHeight(30); for (int i = 0; i < view.getRowCount(); i++) view.expandRow(i);
            JTextArea report = new JTextArea(); report.setEditable(false); report.setLineWrap(true); report.setWrapStyleWord(true); report.setBorder(BorderFactory.createEmptyBorder(12, 12, 12, 12));
            view.addTreeSelectionListener(e -> { Object selected = view.getLastSelectedPathComponent(); String id = nodes.entrySet().stream().filter(entry -> entry.getValue() == selected).map(Map.Entry::getKey).findFirst().orElse(""); if (!id.isEmpty()) report.setText(SessionInventory.details(entries.get(id))); });
            JSplitPane body = new JSplitPane(JSplitPane.HORIZONTAL_SPLIT, new JScrollPane(view), new JScrollPane(report)); body.setPreferredSize(new Dimension(800, 430)); body.setDividerLocation(280);
            p.desktop.showDialog(p.desktop.dialog("Agents", body));
        });
    }
}
