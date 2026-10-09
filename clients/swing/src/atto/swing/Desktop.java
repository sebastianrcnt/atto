package atto.swing;

import javax.swing.*;
import javax.swing.event.*;
import java.awt.*;
import java.awt.datatransfer.*;
import java.awt.event.*;
import java.awt.image.BufferedImage;
import java.io.*;
import java.nio.file.*;
import java.util.*;
import java.util.List;
import java.util.concurrent.*;
import java.util.function.Consumer;
import javax.imageio.ImageIO;
import static atto.swing.Json.*;

final class Desktop {
    final Options options;
    final Settings settings = new Settings();
    final Core core;
    final JFrame window = new JFrame("atto");
    final JTabbedPane tabs = new JTabbedPane();
    final Map<String, SessionPane> sessions = new LinkedHashMap<>();
    final Map<String, Action> actions = new LinkedHashMap<>();
    final JLabel connection = new JLabel("Connecting…");
    final JTextField search = new JTextField();
    final DefaultListModel<Map<String, Object>> sessionList = new DefaultListModel<>();
    final JList<Map<String, Object>> sidebar = new JList<>(sessionList);
    final Map<String, JDialog> prompts = new HashMap<>();
    List<Object> allSessions = List.of();
    final JSplitPane split;
    final List<javax.swing.Timer> timers = new ArrayList<>();
    boolean initialOpened, closing;
    int fontSize;
    final int menuMask = Toolkit.getDefaultToolkit().getMenuShortcutKeyMaskEx();

    Desktop(Options options) {
        this.options = options; core = new Core(options);
        fontSize = Math.max(10, Math.min(28, settings.integer("fontSize", 14)));
        applyTheme(settings.string("theme", "system"));
        window.setDefaultCloseOperation(WindowConstants.DO_NOTHING_ON_CLOSE);
        window.setSize(settings.integer("width", 1200), settings.integer("height", 850));
        window.setLocationByPlatform(true);
        window.addWindowListener(new WindowAdapter() {
            public void windowClosing(WindowEvent e) { quit(); }
            public void windowActivated(WindowEvent e) { window.setTitle("atto"); }
        });
        core.connection = s -> edt(() -> {
            connection.setText(s);
            for (SessionPane pane : sessions.values()) pane.updateControls();
            if (s.equals("Connected")) {
                refreshSessions();
                if (!initialOpened) {
                    initialOpened = true;
                    open(options.session().isEmpty() ? "thread/start" : "thread/resume",
                        options.session().isEmpty() ? map("cwd", options.cwd(), "deferStart", true) : map("threadId", options.session(), "deferStart", true));
                }
            }
        });
        core.changed = snapshot -> edt(() -> update(snapshot));
        core.opened = snapshot -> edt(() -> {
            if (closing) return;
            update(snapshot); SessionPane pane = sessions.get(str(snapshot.get("threadId")));
            tabs.setSelectedComponent(pane); pane.refreshCommands();
            if (!yes(pane.info.get("offline"))) pane.rpc("auth/list", Map.of(), v -> {
                Map<String, Object> login = obj(obj(v).get("login"));
                if (!login.isEmpty()) { Map<String, Object> params = new LinkedHashMap<>(login); params.put("threadId", pane.id); notification(map("method", "auth/updated", "params", params)); }
            });
            // Starting a new/resumed runtime defers executable project content until
            // an explicit trust decision. Existing attached workers are already started.
            if (pane.trustPending) { pane.trustPending = false; trust(pane); }
        });
        core.error = e -> edt(() -> error(e));
        core.event = n -> edt(() -> notification(n));
        registerActions();
        JPanel left = new JPanel(new BorderLayout(0, 6));
        search.putClientProperty("JTextField.placeholderText", "Search sessions");
        search.getDocument().addDocumentListener(listener(this::filterSessions));
        left.add(search, BorderLayout.NORTH); left.add(new JScrollPane(sidebar), BorderLayout.CENTER);
        JPanel sessionButtons = new JPanel(new FlowLayout(FlowLayout.LEFT));
        sessionButtons.add(button("New", () -> newSession())); sessionButtons.add(button("Refresh", this::refreshSessions));
        left.add(sessionButtons, BorderLayout.SOUTH);
        sidebar.setCellRenderer(new DefaultListCellRenderer() {
            public Component getListCellRendererComponent(JList<?> l, Object value, int index, boolean selected, boolean focus) {
                JLabel label = (JLabel)super.getListCellRendererComponent(l, value, index, selected, focus);
                Map<String, Object> row = obj(value);
                String name = str(row.get("name")); if (name.isEmpty()) name = str(row.get("threadId"));
                label.setText("<html><b>" + Markdown.escape(name) + "</b> · " + (yes(row.get("busy")) ? "Working" : "Idle")
                    + (yes(row.get("loaded")) ? " · live" : "") + "<br><small>" + Markdown.escape(str(row.get("cwd")))
                    + "<br>" + Markdown.escape(str(row.get("updatedAt"))) + "</small></html>");
                label.setBorder(BorderFactory.createEmptyBorder(6, 6, 6, 6)); return label;
            }
        });
        sidebar.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { if (e.getClickCount() == 2) resumeSelected(); } });
        bind(sidebar, "ENTER", "resume", this::resumeSelected);
        split = new JSplitPane(JSplitPane.HORIZONTAL_SPLIT, left, tabs); split.setResizeWeight(0);
        split.setDividerLocation(settings.integer("sidebarWidth", 250));
        window.add(split, BorderLayout.CENTER);
        JToolBar toolbar = new JToolBar(); toolbar.setFloatable(false);
        for (String name : List.of("New session", "Model", "Effort", "Tree", "Jobs", "Agents", "Context", "Goal", "Command palette"))
            toolbar.add(new JButton(actions.get(name)));
        toolbar.addSeparator(); toolbar.add(connection); window.add(toolbar, BorderLayout.NORTH);
        JMenuBar menu = new JMenuBar();
        JMenu file = new JMenu("Session"), tools = new JMenu("Actions"), view = new JMenu("View");
        for (var e : actions.entrySet()) {
            JMenu target = e.getKey().contains("session") || e.getKey().equals("Quit") ? file : e.getKey().contains("font") || e.getKey().contains("theme") ? view : tools;
            target.add(new JMenuItem(e.getValue()));
        }
        menu.add(file); menu.add(tools); menu.add(view); window.setJMenuBar(menu);
        startTimer(new javax.swing.Timer(1000, e -> sessions.values().forEach(SessionPane::refreshStatus)));
        startTimer(new javax.swing.Timer(10000, e -> { if (core.protocol.ready) refreshSessions(); }));
    }
    void startTimer(javax.swing.Timer timer) { timers.add(timer); timer.start(); }
    void show() { SwingUtilities.updateComponentTreeUI(window); window.setVisible(true); core.connect(); }
    static void edt(Runnable task) { if (SwingUtilities.isEventDispatchThread()) task.run(); else SwingUtilities.invokeLater(task); }
    static JButton button(String text, Runnable action) { JButton b = new JButton(text); b.addActionListener(e -> action.run()); return b; }
    static DocumentListener listener(Runnable action) {
        return new DocumentListener() { public void insertUpdate(DocumentEvent e) { action.run(); } public void removeUpdate(DocumentEvent e) { action.run(); } public void changedUpdate(DocumentEvent e) { action.run(); } };
    }
    static void bind(JComponent c, String key, String name, Runnable task) {
        c.getInputMap(JComponent.WHEN_ANCESTOR_OF_FOCUSED_COMPONENT).put(KeyStroke.getKeyStroke(key), name);
        c.getActionMap().put(name, new AbstractAction() { public void actionPerformed(ActionEvent e) { task.run(); } });
    }
    void action(String name, int key, int extra, Runnable task) {
        Action a = new AbstractAction(name) { public void actionPerformed(ActionEvent e) { task.run(); } };
        if (key != 0) {
            KeyStroke stroke = KeyStroke.getKeyStroke(key, menuMask | extra); a.putValue(Action.ACCELERATOR_KEY, stroke);
            window.getRootPane().getInputMap(JComponent.WHEN_IN_FOCUSED_WINDOW).put(stroke, name);
            window.getRootPane().getActionMap().put(name, a);
        }
        actions.put(name, a);
    }
    void registerActions() {
        action("New session", KeyEvent.VK_N, 0, this::newSession);
        action("Resume session", KeyEvent.VK_O, 0, () -> { search.requestFocusInWindow(); sidebar.requestFocusInWindow(); });
        action("Detach session", KeyEvent.VK_W, 0, () -> withPane(p -> detach(p, false)));
        action("Close session and stop work", KeyEvent.VK_W, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> detach(p, true)));
        action("Rename session", KeyEvent.VK_R, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> input("Session name", str(p.info.get("name")), text -> p.rpc("thread/setName", map("name", text), v -> p.hydrate()))));
        action("Model", KeyEvent.VK_M, 0, () -> withPane(p -> Panels.models(p)));
        action("Effort", KeyEvent.VK_E, 0, () -> withPane(p -> Panels.effort(p)));
        action("Tree", KeyEvent.VK_T, 0, () -> withPane(p -> Panels.tree(p, false)));
        action("Fork session", KeyEvent.VK_F, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> Panels.tree(p, true)));
        action("Jobs", KeyEvent.VK_J, 0, () -> withPane(p -> Panels.jobs(p)));
        action("Timers", KeyEvent.VK_T, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> Panels.timers(p)));
        action("Agents", KeyEvent.VK_A, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> Panels.agents(p)));
        action("Context", KeyEvent.VK_I, 0, () -> withPane(p -> p.rpc("thread/context", Map.of(), v -> text("Context breakdown", Panels.pretty(v), null))));
        action("System context", 0, 0, () -> withPane(p -> p.rpc("thread/context", map("view", "system"), v -> text("System context", Panels.pretty(v), null))));
        action("Compact", 0, 0, () -> withPane(p -> p.rpc("thread/compact", Map.of(), v -> {})));
        action("Reload", KeyEvent.VK_R, 0, () -> withPane(p -> p.rpc("thread/reload", Map.of(), v -> {})));
        action("Request: save last provider request", 0, 0, () -> withPane(p -> p.rpc("thread/debugRequest", Map.of(), v -> save("request.json", str(obj(v).get("request"))))));
        action("Debug: save runtime profiles and requests", 0, 0, () -> withPane(Panels::debug));
        action("Archive session", 0, 0, () -> withPane(p -> {
            if (JOptionPane.showConfirmDialog(window, "Archive saved session and stop its background work?", "Archive", JOptionPane.YES_NO_OPTION) == JOptionPane.YES_OPTION)
                p.rpc("thread/archive", Map.of(), v -> { remove(p); newSession(); });
        }));
        action("Reconnect", 0, 0, core.protocol::reconnect);
        action("Copy last answer", KeyEvent.VK_C, InputEvent.SHIFT_DOWN_MASK, () -> withPane(this::copyLast));
        action("Usage totals", 0, 0, () -> withPane(p -> text("Usage", Panels.pretty(p.info.get("usage")), null)));
        action("Goal", KeyEvent.VK_G, 0, () -> withPane(p -> Panels.goal(p)));
        action("Interrupt", 0, 0, () -> withPane(p -> p.interrupt(false)));
        action("Hard cancel", 0, 0, () -> withPane(p -> p.interrupt(true)));
        action("Background running command", KeyEvent.VK_B, 0, () -> withPane(p -> p.rpc("turn/background", Map.of(), v -> {})));
        action("Queue input", KeyEvent.VK_Q, 0, () -> withPane(p -> p.submit("queue")));
        action("Resume queue", 0, 0, () -> withPane(p -> p.rpc("queue/resume", Map.of(), v -> {})));
        action("Take back pending input", KeyEvent.VK_UP, InputEvent.ALT_DOWN_MASK, () -> withPane(p -> p.takeback(null)));
        action("Attach image", KeyEvent.VK_U, 0, () -> withPane(SessionPane::chooseImages));
        action("Paste image", KeyEvent.VK_V, InputEvent.SHIFT_DOWN_MASK, () -> withPane(SessionPane::pasteImage));
        action("Next session", KeyEvent.VK_CLOSE_BRACKET, 0, () -> { if (tabs.getTabCount() > 0) tabs.setSelectedIndex((tabs.getSelectedIndex() + 1) % tabs.getTabCount()); });
        action("Previous session", KeyEvent.VK_OPEN_BRACKET, 0, () -> { if (tabs.getTabCount() > 0) tabs.setSelectedIndex((tabs.getSelectedIndex() + tabs.getTabCount() - 1) % tabs.getTabCount()); });
        action("Focus composer", KeyEvent.VK_L, 0, () -> withPane(p -> p.composer.requestFocusInWindow()));
        action("Jump to bottom", KeyEvent.VK_END, 0, () -> withPane(p -> p.transcript.bottom()));
        action("Command palette", KeyEvent.VK_K, 0, this::palette);
        action("Increase font", KeyEvent.VK_EQUALS, 0, () -> changeFont(1));
        action("Decrease font", KeyEvent.VK_MINUS, 0, () -> changeFont(-1));
        action("Choose theme", 0, 0, () -> pick("Theme", List.of("system", "light", "dark"), x -> x, theme -> {
            settings.values.put("theme", theme); applyTheme(theme); SwingUtilities.updateComponentTreeUI(window);
            sessions.values().forEach(p -> p.transcript.invalidateRows()); persist();
        }));
        action("Authentication status", 0, 0, () -> withPane(p -> p.rpc("auth/list", Map.of(), v -> text("Authentication", Panels.pretty(v), null))));
        action("Login", 0, 0, () -> withPane(p -> Panels.login(p, "")));
        action("Logout", 0, 0, () -> withPane(p -> Panels.logout(p, "")));
        action("Cancel login", 0, 0, () -> withPane(p -> p.rpc("auth/cancel", Map.of(), v -> {})));
        action("Quit", KeyEvent.VK_Q, InputEvent.SHIFT_DOWN_MASK, this::quit);
        bind(window.getRootPane(), "ESCAPE", "interrupt", () -> withPane(p -> p.interrupt(false)));
    }
    void withPane(Consumer<SessionPane> action) {
        if (tabs.getSelectedComponent() instanceof SessionPane pane) action.accept(pane);
        else error("Open a session first.");
    }
    void newSession() {
        input("Working directory (on server)", options.cwd(), cwd -> open("thread/start", map("cwd", cwd, "deferStart", true)));
    }
    void open(String method, Map<String, Object> params) {
        core.hydrate(method, params).exceptionally(e -> null);
    }
    void trust(SessionPane pane) {
        if (!pane.writable()) return;
        pane.rpc("thread/context", Map.of(), v -> {
            JTextArea content = new JTextArea("Starting a session can execute trusted project hooks/extensions.\nInspect this context before allowing startup:\n\n" + Panels.pretty(v), 20, 80);
            content.setEditable(false);
            JDialog dialog = dialog("Project trust", new JScrollPane(content));
            JPanel buttons = new JPanel();
            buttons.add(button("Allow session startup", () -> { dialog.dispose(); pane.rpc("thread/sessionStart", Map.of(), x -> {}); }));
            buttons.add(button("Detach", () -> { dialog.dispose(); detach(pane, false); }));
            dialog.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true);
        });
    }
    void update(Map<String, Object> snapshot) {
        if (closing) return;
        String id = str(snapshot.get("threadId")); if (id.isEmpty()) return;
        SessionPane pane = sessions.get(id);
        if (pane == null) { pane = new SessionPane(this, id); sessions.put(id, pane); tabs.addTab(id, pane); }
        pane.update(snapshot);
        int index = tabs.indexOfComponent(pane); String name = str(snapshot.get("name"));
        tabs.setTitleAt(index, (yes(snapshot.get("busy")) ? "● " : "") + (name.isEmpty() ? id : name));
    }
    void refreshSessions() {
        core.call("thread/list", "", Map.of()).whenComplete((v, e) -> edt(() -> {
            if (e != null) { connection.setText(e.getMessage()); return; }
            allSessions = Json.list(obj(v).get("threads")); filterSessions();
        }));
    }
    void filterSessions() {
        String query = search.getText().toLowerCase(Locale.ROOT); sessionList.clear();
        for (Object row : allSessions) if (write(row).toLowerCase(Locale.ROOT).contains(query)) sessionList.addElement(obj(row));
    }
    void resumeSelected() { if (sidebar.getSelectedValue() != null) open("thread/resume", map("threadId", sidebar.getSelectedValue().get("threadId"), "deferStart", true)); }
    void detach(SessionPane pane, boolean close) {
        if (close) {
            if (JOptionPane.showConfirmDialog(window, "End session and stop its jobs? Detach instead to leave work running.", "Close session", JOptionPane.YES_NO_OPTION) != JOptionPane.YES_OPTION) return;
            pane.rpc("thread/close", map("reason", "close"), v -> remove(pane));
        } else { remove(pane); }
    }
    void remove(SessionPane pane) {
        core.detach(pane.id);
        sessions.remove(pane.id); tabs.remove(pane); pane.transcript.close();
        JDialog prompt = prompts.remove(pane.id); if (prompt != null) prompt.dispose(); refreshSessions();
    }
    void notification(Map<String, Object> n) {
        Map<String, Object> p = obj(n.get("params")); String id = str(p.get("threadId")); SessionPane pane = sessions.get(id);
        String method = str(n.get("method"));
        if (method.equals("input/recovered") && pane != null && str(p.get("clientId")).equals(core.protocol.clientId)) {
            if (!yes(p.get("ifEmpty")) || pane.composer.getText().isEmpty()) pane.composer.setText(str(p.get("text")));
            pane.images.addAll(Json.list(p.get("images"))); pane.updateAttachments();
        }
        if (method.equals("commands/changed") && pane != null) pane.refreshCommands();
        if (method.equals("thread/reloaded") && pane != null) pane.statusFingerprint = "";
        if (method.equals("turn/completed")) attention("Turn finished", id);
        if (method.equals("auth/updated") && pane != null) {
            connection.setText("Login " + str(p.get("provider")) + " · " + str(p.get("status")) + " " + str(p.get("note")));
            String url = str(p.get("url"));
            if (Markdown.safeLink(url)) {
                JEditorPane link = new JEditorPane("text/html", "<html>Continue sign-in in your browser:<br><a href='" + Markdown.escape(url) + "'>" + Markdown.escape(url) + "</a></html>");
                link.setEditable(false);
                JDialog dialog = dialog("Sign in to " + str(p.get("provider")), new JScrollPane(link));
                JPanel buttons = new JPanel(); buttons.add(button("Open browser", () -> CompletableFuture.runAsync(() -> {
                    try { java.awt.Desktop.getDesktop().browse(java.net.URI.create(url)); } catch (Exception e) { edt(() -> error(e.getMessage())); }
                }))); buttons.add(button("Copy URL", () -> copy(url))); dialog.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true);
            }
            if (p.containsKey("error")) error(str(p.get("error")));
        }
        if (method.equals("extension/notify")) {
            connection.setText(str(p.get("message"))); attention(str(p.get("message")), id);
        }
    }
    void attention(String message, String id) {
        if (!window.isFocused()) {
            window.setTitle("● atto · " + message + " · " + id);
            if (yes(settings.values.get("sound"))) Toolkit.getDefaultToolkit().beep();
            try { if (Taskbar.isTaskbarSupported()) Taskbar.getTaskbar().requestUserAttention(false, true); } catch (UnsupportedOperationException ignored) {}
        }
    }
    void prompt(SessionPane pane, Map<String, Object> prompt) {
        JDialog old = prompts.get(pane.id); String promptId = str(prompt.get("id"));
        if (old != null && promptId.equals(old.getRootPane().getClientProperty("promptId"))) return;
        if (old != null) { old.dispose(); prompts.remove(pane.id); }
        if (promptId.isEmpty()) return;
        JPanel body = new JPanel(new BorderLayout(6, 6));
        body.add(new JLabel(str(prompt.get("subtitle")) + " " + str(prompt.get("note"))), BorderLayout.NORTH);
        JTextField input = new JTextField(str(prompt.get("text")), 40);
        List<Object> options = Json.list(prompt.get("options"));
        JList<String> choices = new JList<>(options.stream().map(x -> str(obj(x).get("label")) + " " + str(obj(x).get("description"))).toArray(String[]::new));
        boolean multi = "multiSelect".equals(prompt.get("kind"));
        choices.setSelectionMode(multi ? ListSelectionModel.MULTIPLE_INTERVAL_SELECTION : ListSelectionModel.SINGLE_SELECTION); choices.setSelectedIndex((int)num(prompt.get("selected")));
        boolean select = multi || "select".equals(prompt.get("kind"));
        body.add(select ? new JScrollPane(choices) : input, BorderLayout.CENTER);
        JDialog dialog = dialog(str(prompt.get("title")) + " · " + pane.id, body);
        dialog.getRootPane().putClientProperty("promptId", promptId); prompts.put(pane.id, dialog);
        JPanel buttons = new JPanel();
        Runnable answer = () -> {
            Map<String, Object> params = map("id", promptId);
            if (multi) params.put("indexes", Arrays.stream(choices.getSelectedIndices()).boxed().toList());
            else if (select) { if (choices.getSelectedIndex() < 0) return; params.put("index", choices.getSelectedIndex()); }
            else params.put("text", input.getText());
            pane.rpc("prompt/answer", params, v -> {});
        };
        buttons.add(button("Answer", answer)); buttons.add(button("Cancel", () -> pane.rpc("prompt/answer", map("id", promptId, "cancel", true), v -> {})));
        dialog.addWindowListener(new WindowAdapter() { public void windowClosing(WindowEvent e) { pane.rpc("prompt/answer", map("id", promptId, "cancel", true), v -> {}); } });
        bind(body, "ENTER", "answer", answer);
        dialog.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true); attention("Prompt waiting", pane.id);
    }
    JDialog dialog(String title, Component body) {
        JDialog dialog = new JDialog(window, title, false); dialog.setDefaultCloseOperation(WindowConstants.DISPOSE_ON_CLOSE);
        dialog.add(body, BorderLayout.CENTER); dialog.pack(); dialog.setLocationRelativeTo(window); return dialog;
    }
    void input(String title, String initial, Consumer<String> answer) {
        JTextField field = new JTextField(initial, 45); JDialog dialog = dialog(title, field);
        Runnable submit = () -> { String text = field.getText(); dialog.dispose(); answer.accept(text); };
        dialog.add(button("OK", submit), BorderLayout.SOUTH); field.addActionListener(e -> submit.run());
        dialog.pack(); dialog.setVisible(true); field.requestFocusInWindow();
    }
    void text(String title, String value, Runnable extra) {
        JTextArea area = new JTextArea(value, 25, 85); area.setEditable(false); area.setFont(new Font(Font.MONOSPACED, Font.PLAIN, fontSize));
        JDialog dialog = dialog(title, new JScrollPane(area)); JPanel buttons = new JPanel();
        buttons.add(button("Copy", () -> copy(value))); buttons.add(button("Save…", () -> save("atto.txt", value)));
        if (extra != null) buttons.add(button("Refresh", extra)); dialog.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true);
    }
    <T> void pick(String title, List<T> entries, java.util.function.Function<T, String> label, Consumer<T> select) {
        JTextField query = new JTextField(); DefaultListModel<T> model = new DefaultListModel<>(); JList<T> list = new JList<>(model);
        list.setSelectionMode(ListSelectionModel.SINGLE_SELECTION);
        list.setCellRenderer(new DefaultListCellRenderer() { public Component getListCellRendererComponent(JList<?> l, Object v, int i, boolean selected, boolean focus) {
            @SuppressWarnings("unchecked") T entry = (T)v;
            return super.getListCellRendererComponent(l, label.apply(entry), i, selected, focus);
        } });
        Runnable filter = () -> { model.clear(); for (T entry : entries) if (label.apply(entry).toLowerCase(Locale.ROOT).contains(query.getText().toLowerCase(Locale.ROOT))) model.addElement(entry); if (!model.isEmpty()) list.setSelectedIndex(0); };
        query.getDocument().addDocumentListener(listener(filter)); filter.run();
        JPanel body = new JPanel(new BorderLayout()); body.add(query, BorderLayout.NORTH); body.add(new JScrollPane(list), BorderLayout.CENTER); body.setPreferredSize(new Dimension(700, 420));
        JDialog dialog = dialog(title, body);
        SessionPane owner = tabs.getSelectedComponent() instanceof SessionPane p ? p : null;
        if (owner != null && owner.writable()) {
            owner.rpc("client/gate", map("open", true), v -> {});
            dialog.addWindowListener(new WindowAdapter() { public void windowClosed(WindowEvent e) { owner.rpc("client/gate", map("open", false), v -> {}); } });
        }
        Runnable choose = () -> { T entry = list.getSelectedValue(); if (entry != null) { dialog.dispose(); select.accept(entry); } };
        bind(body, "ENTER", "choose", choose); bind(body, "ESCAPE", "dismiss", dialog::dispose);
        bind(query, "DOWN", "down", () -> { list.requestFocusInWindow(); if (list.getSelectedIndex() < 0) list.setSelectedIndex(0); });
        list.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { if (e.getClickCount() == 2) choose.run(); } });
        dialog.add(button("Select", choose), BorderLayout.SOUTH); dialog.pack(); dialog.setVisible(true); query.requestFocusInWindow();
    }
    void palette() {
        List<String> entries = new ArrayList<>(actions.keySet());
        SessionPane pane = tabs.getSelectedComponent() instanceof SessionPane p ? p : null;
        if (pane != null) for (Object c : pane.commands) entries.add("/" + str(obj(c).get("name")) + " " + str(obj(c).get("description")));
        pick("Command palette", entries, x -> x, selected -> {
            if (selected.startsWith("/") && pane != null) { pane.composer.setText(selected.split(" ", 2)[0] + " "); pane.composer.requestFocusInWindow(); }
            else actions.get(selected).actionPerformed(new ActionEvent(this, 0, selected));
        });
    }
    void copyLast(SessionPane pane) {
        String last = ""; for (Object item : Json.list(pane.info.get("items"))) if ("agentMessage".equals(obj(item).get("type"))) last = str(obj(item).get("text")); copy(last);
    }
    static void copy(String text) { Toolkit.getDefaultToolkit().getSystemClipboard().setContents(new StringSelection(text), null); }
    void save(String suggested, String text) {
        JFileChooser chooser = new JFileChooser(); chooser.setSelectedFile(new File(suggested));
        if (chooser.showSaveDialog(window) == JFileChooser.APPROVE_OPTION) {
            Path file = chooser.getSelectedFile().toPath();
            CompletableFuture.runAsync(() -> { try { Files.writeString(file, text); } catch (IOException e) { edt(() -> error(e.getMessage())); } });
        }
    }
    void error(String message) { if (closing) return; connection.setText(message); text("atto: " + message, message, null); }
    void changeFont(int delta) {
        fontSize = Math.max(10, Math.min(28, fontSize + delta)); settings.values.put("fontSize", fontSize);
        sessions.values().forEach(p -> { p.composer.setFont(new Font(Font.MONOSPACED, Font.PLAIN, fontSize)); p.transcript.invalidateRows(); }); persist();
    }
    void applyTheme(String theme) {
        if (theme.equals("system")) {
            for (String key : List.of("Panel", "Viewport", "TextArea", "TextPane", "EditorPane", "List", "TextField", "TabbedPane", "ScrollPane", "Label")) {
                UIManager.put(key + ".background", null); UIManager.put(key + ".foreground", null);
            }
            try { UIManager.setLookAndFeel(UIManager.getSystemLookAndFeelClassName()); } catch (Exception e) { System.err.println(e.getMessage()); }
            return;
        }
        boolean dark = theme.equals("dark"); Color bg = dark ? new Color(30, 32, 36) : Color.WHITE;
        Color fg = dark ? new Color(225, 228, 233) : new Color(30, 32, 36);
        for (String key : List.of("Panel", "Viewport", "TextArea", "TextPane", "EditorPane", "List", "TextField", "TabbedPane", "ScrollPane", "Label")) {
            UIManager.put(key + ".background", bg); UIManager.put(key + ".foreground", fg);
        }
    }
    static String savedEndpoint(String address) {
        if (address.isEmpty() || address.startsWith("unix://")) return address.split("[?#]", 2)[0];
        try { var uri = java.net.URI.create(address); return new java.net.URI(uri.getScheme(), null, uri.getHost(), uri.getPort(), uri.getPath(), null, null).toString(); }
        catch (Exception e) { return ""; }
    }
    void persist() { Map<String, Object> values = obj(Json.copy(settings.values)); CompletableFuture.runAsync(() -> { synchronized (settings) { settings.save(values); } }); }
    void quit() {
        if (closing) return; closing = true;
        settings.values.put("width", window.getWidth()); settings.values.put("height", window.getHeight()); settings.values.put("sidebarWidth", split.getDividerLocation());
        // Never persist the bearer token.
        settings.values.put("connection", savedEndpoint(options.connect())); settings.values.put("atto", options.atto()); persist();
        for (SessionPane p : sessions.values()) p.transcript.close();
        timers.forEach(javax.swing.Timer::stop);
        core.close(); window.dispose();
    }
}

final class SessionPane extends JPanel {
    final Desktop desktop; final String id;
    final Transcript transcript;
    final JTextArea composer = new JTextArea(4, 60);
    final JPanel pending = new JPanel(), attachments = new JPanel();
    final JLabel status = new JLabel(" "), customStatus = new JLabel(" "), extensions = new JLabel(" ");
    String statusFingerprint = "";
    boolean statusPending;
    long statusRefreshAt;
    int statusRefreshInterval;
    final JTextArea goal = new JTextArea();
    final List<Object> images = new ArrayList<>();
    final List<JButton> writeButtons = new ArrayList<>();
    Map<String, Object> info = Map.of(); List<Object> commands = List.of();
    boolean trustPending = true;
    SessionPane(Desktop desktop, String id) {
        super(new BorderLayout(6, 6)); this.desktop = desktop; this.id = id;
        transcript = new Transcript(this); add(transcript, BorderLayout.CENTER);
        composer.setLineWrap(true); composer.setWrapStyleWord(true); composer.setFont(new Font(Font.MONOSPACED, Font.PLAIN, desktop.fontSize));
        pending.setLayout(new BoxLayout(pending, BoxLayout.Y_AXIS)); attachments.setLayout(new FlowLayout(FlowLayout.LEFT));
        JPanel bottom = new JPanel(new BorderLayout(4, 4)), editor = new JPanel(new BorderLayout());
        editor.add(new JScrollPane(composer), BorderLayout.CENTER); JPanel controls = new JPanel(new FlowLayout(FlowLayout.LEFT));
        for (var pair : List.of(new Object[]{"Send ↵", (Runnable)() -> submit("auto")}, new Object[]{"Queue", (Runnable)() -> submit("queue")},
                new Object[]{"Send now", (Runnable)() -> submit("replace")}, new Object[]{"Stop (Esc)", (Runnable)() -> interrupt(false)},
                new Object[]{"Hard cancel", (Runnable)() -> interrupt(true)}, new Object[]{"Image…", (Runnable)this::chooseImages},
                new Object[]{"Complete", (Runnable)this::complete})) {
            JButton b = Desktop.button((String)pair[0], (Runnable)pair[1]); controls.add(b); writeButtons.add(b);
        }
        controls.add(Desktop.button("Detach tab", () -> desktop.detach(this, false)));
        editor.add(controls, BorderLayout.SOUTH); JPanel north = new JPanel(new BorderLayout()); north.add(pending, BorderLayout.CENTER); north.add(attachments, BorderLayout.SOUTH);
        bottom.add(north, BorderLayout.NORTH); bottom.add(editor, BorderLayout.CENTER);
        JPanel foot = new JPanel(new GridLayout(0, 1)); foot.add(status); foot.add(customStatus); foot.add(extensions); customStatus.setVisible(false); bottom.add(foot, BorderLayout.SOUTH); add(bottom, BorderLayout.SOUTH);
        goal.setEditable(false); goal.setLineWrap(true); goal.setWrapStyleWord(true); goal.setRows(3); goal.setVisible(false); add(goal, BorderLayout.NORTH);
        composer.getInputMap().put(KeyStroke.getKeyStroke("ENTER"), "send"); composer.getActionMap().put("send", new AbstractAction() { public void actionPerformed(ActionEvent e) { submit("auto"); } });
        composer.getInputMap().put(KeyStroke.getKeyStroke("shift ENTER"), "insert-break");
        composer.getInputMap().put(KeyStroke.getKeyStroke(KeyEvent.VK_ENTER, desktop.menuMask), "now"); composer.getActionMap().put("now", new AbstractAction() { public void actionPerformed(ActionEvent e) { submit("replace"); } });
        Desktop.bind(composer, "shift LEFT", "takeback", () -> takeback(null)); Desktop.bind(composer, "alt UP", "takeback-alt", () -> takeback(null));
        Desktop.bind(composer, "ctrl TAB", "focus-next", composer::transferFocus);
        Desktop.bind(composer, "ctrl SPACE", "complete", this::complete); Desktop.bind(composer, "TAB", "queue", () -> submit("queue")); composer.setFocusTraversalKeysEnabled(false);
        composer.setTransferHandler(new TransferHandler() {
            public boolean canImport(TransferSupport support) {
                return support.isDataFlavorSupported(DataFlavor.javaFileListFlavor) || support.isDataFlavorSupported(DataFlavor.imageFlavor) || support.isDataFlavorSupported(DataFlavor.stringFlavor);
            }
            public boolean importData(TransferSupport support) {
                try {
                    if (support.isDataFlavorSupported(DataFlavor.javaFileListFlavor)) {
                        @SuppressWarnings("unchecked") List<File> files = (List<File>)support.getTransferable().getTransferData(DataFlavor.javaFileListFlavor);
                        attachFiles(files); return true;
                    }
                    if (support.isDataFlavorSupported(DataFlavor.imageFlavor)) { attachImage((Image)support.getTransferable().getTransferData(DataFlavor.imageFlavor)); return true; }
                    composer.replaceSelection((String)support.getTransferable().getTransferData(DataFlavor.stringFlavor)); return true;
                } catch (Exception e) { desktop.error(e.getMessage()); return false; }
            }
        });
        javax.swing.Timer completion = new javax.swing.Timer(250, e -> autoComplete()); completion.setRepeats(false);
        composer.getDocument().addDocumentListener(Desktop.listener(completion::restart));
    }
    void update(Map<String, Object> snapshot) {
        info = snapshot; transcript.update(Json.list(snapshot.get("items"))); updatePending(); updateControls(); refreshStatus();
        Map<String, Object> g = obj(info.get("goal")); goal.setVisible(!g.isEmpty());
        goal.setText("Goal: " + str(g.get("objective")) + "\n" + str(g.get("statusLabel")) + " · " + str(g.get("summary")) + " · " + str(g.get("tokensUsed")) + " tokens\n" + str(g.get("note")));
        desktop.prompt(this, obj(info.get("prompt")));
    }
    boolean writable() { return desktop.core.protocol.ready && str(info.get("readOnly")).isEmpty() && !yes(info.get("offline")) && !yes(info.get("closed")); }
    void updateControls() { composer.setEditable(writable()); writeButtons.forEach(b -> b.setEnabled(writable())); }
    void refreshStatus() {
        Map<String, Object> usage = obj(info.get("usage")), activity = obj(info.get("activity"));
        long context = num(info.get("contextTokens")), window = num(info.get("contextWindow")), input = num(usage.get("inputTokens")), cached = num(usage.get("cachedInputTokens"));
        long started = num(activity.get("startedAt")); if (started == 0) started = num(obj(info.get("turn")).get("startedAt"));
        boolean shellRunning = Json.list(info.get("items")).stream().anyMatch(x -> yes(obj(x).get("shell")) && "inProgress".equals(obj(x).get("status")));
        String busy = yes(info.get("busy")) ? str(activity.getOrDefault("phase", "Working")) + " " + (started > 0 ? (System.currentTimeMillis() - started) / 1000 : 0) + "s" : shellRunning ? "Shell running" : "Idle";
        status.setText(str(info.get("model")) + " · " + str(info.get("effort")) + " · context " + context + (window > 0 ? "/" + window + " (" + context * 100 / window + "%)" : "")
            + " · cache " + (input > 0 ? cached * 100 / input : 0) + "% · in/out " + input + "/" + num(usage.get("outputTokens")) + " · $" + str(usage.getOrDefault("cost", 0))
            + " · " + busy + " · jobs " + num(info.get("jobs")) + " timers " + num(info.get("timers")) + " · " + str(info.get("cwd")) + (writable() ? "" : " · READ ONLY / OFFLINE"));
        Map<String, Object> ui = obj(info.get("extensionUi")); List<String> lines = new ArrayList<>();
        for (Object s : Json.list(ui.get("status"))) lines.add(str(obj(s).get("text")));
        for (Object w : Json.list(ui.get("widgets"))) for (Object line : Json.list(obj(w).get("lines"))) lines.add(str(line));
        extensions.setText("<html>" + Markdown.escape(String.join("\n", lines)).replace("\n", "<br>") + "</html>");
        String fingerprint = write(map("model", info.get("model"), "effort", info.get("effort"), "usage", usage, "context", context, "busy", info.get("busy"), "name", info.get("name"), "cwd", info.get("cwd")));
        long now = System.currentTimeMillis();
        if (writable() && !statusPending && (!fingerprint.equals(statusFingerprint) || (statusRefreshInterval > 0 && now >= statusRefreshAt))) {
            statusPending = true; statusFingerprint = fingerprint;
            desktop.core.call("thread/statusLine", id, Map.of()).whenComplete((v, e) -> Desktop.edt(() -> {
                statusPending = false;
                if (e != null) { customStatus.setText("statusLine: " + e.getMessage()); customStatus.setVisible(true); return; }
                Map<String, Object> result = obj(v); statusRefreshInterval = (int)Math.min(86400, Math.max(0, num(result.get("refreshInterval"))));
                statusRefreshAt = System.currentTimeMillis() + Math.max(1, statusRefreshInterval) * 1000L;
                String output = String.join("\n", Json.list(result.get("lines")).stream().map(Json::str).toList());
                output = output.replaceAll("\u001b\\[[0-9;?]*[ -/]*[@-~]", "");
                customStatus.setText("<html>" + Markdown.escape(output).replace("\n", "<br>") + "</html>"); customStatus.setVisible(yes(result.get("configured")));
            }));
        }
    }
    void rpc(String method, Map<String, Object> params, Consumer<Object> answer) {
        desktop.core.call(method, id, yes(info.get("offline")) && (method.equals("thread/read") || method.equals("thread/tree") || method.equals("item/image") || method.equals("item/output")) ? withOffline(params) : params).whenComplete((v, e) -> Desktop.edt(() -> {
            if (desktop.closing) return;
            if (e != null) desktop.error(e.getMessage()); else answer.accept(v);
        }));
    }
    Map<String, Object> withOffline(Map<String, Object> params) { Map<String, Object> result = new LinkedHashMap<>(params); result.put("offline", true); return result; }
    void hydrate() { desktop.open("thread/read", map("threadId", id)); }
    void refreshCommands() { if (yes(info.get("offline"))) return; rpc("commands/list", Map.of(), v -> commands = Json.list(obj(v).get("commands"))); }
    void submit(String intent) {
        if (!writable()) return; String text = composer.getText(); List<Object> sentImages = List.copyOf(images);
        if (text.startsWith("/") && intent.equals("auto") && local(text)) { composer.setText(""); return; }
        composer.setText(""); images.clear(); updateAttachments();
        desktop.core.call("input/submit", id, map("input", text, "intent", intent, "images", sentImages)).whenComplete((v, e) -> Desktop.edt(() -> {
            if (e != null) {
                if (composer.getText().isEmpty()) { composer.setText(text); images.addAll(sentImages); updateAttachments(); }
                desktop.error(e.getMessage() + "\nThe request may have been accepted; inspect the transcript before retrying.");
            }
        }));
    }
    boolean local(String text) {
        String[] parts = text.substring(1).trim().split("\\s+", 2); String name = parts[0], arg = parts.length == 2 ? parts[1] : "";
        String typed = name;
        List<String> names = commands.stream().map(c -> str(obj(c).get("name"))).filter(n -> n.startsWith(typed)).toList();
        if (!names.contains(name) && names.size() == 1) name = names.getFirst();
        switch (name) {
            case "copy" -> desktop.copyLast(this);
            case "model" -> { if (!arg.isEmpty()) return false; Panels.models(this); }
            case "effort" -> { if (!arg.isEmpty()) return false; Panels.effort(this); }
            case "resume", "sessions" -> desktop.actions.get("Resume session").actionPerformed(null);
            case "tree" -> Panels.tree(this, false);
            case "fork" -> Panels.tree(this, true);
            case "clear", "new" -> desktop.newSession();
            case "jobs" -> Panels.jobs(this);
            case "timers" -> Panels.timers(this);
            case "agents" -> Panels.agents(this);
            case "context" -> { if (!arg.isEmpty() && !arg.equals("system")) return false; rpc("thread/context", arg.isEmpty() ? Map.of() : map("view", "system"), v -> desktop.text("Context", Panels.pretty(v), null)); }
            case "goal" -> {
                if (arg.isEmpty() || arg.equals("show") || arg.equals("edit")) Panels.goal(this);
                else if (arg.startsWith("set ")) rpc("goal/set", map("input", arg.substring(4)), v -> {});
                else return false;
            }
            case "detach" -> desktop.detach(this, false);
            case "close" -> desktop.detach(this, true);
            case "quit", "exit" -> desktop.quit();
            case "request" -> desktop.actions.get("Request: save last provider request").actionPerformed(null);
            case "debug" -> desktop.actions.get("Debug: save runtime profiles and requests").actionPerformed(null);
            case "login" -> Panels.login(this, arg);
            case "logout" -> Panels.logout(this, arg);
            case "tui" -> desktop.error("/tui selects a terminal renderer; Swing has theme and font actions in the palette.");
            case "remote" -> desktop.error("Swing does not own the server listener. Run atto app-server --listen ws://… or atto serve, then connect clients to it.");
            case "archive" -> desktop.actions.get("Archive session").actionPerformed(null);
            default -> { return false; }
        }
        return true;
    }
    void interrupt(boolean cancel) { rpc("turn/interrupt", cancel ? map("mode", "cancel") : Map.of(), v -> {}); }
    void takeback(String inputId) {
        rpc("turn/unsteer", inputId == null ? Map.of() : map("inputId", inputId), v -> {
            Map<String, Object> result = obj(v); composer.setText(str(result.get("text")) + (composer.getText().isEmpty() ? "" : "\n" + composer.getText())); images.addAll(Json.list(result.get("images"))); updateAttachments(); composer.requestFocusInWindow();
        });
    }
    void updatePending() {
        pending.removeAll(); Map<String, Object> p = obj(info.get("pending"));
        for (Object row : Json.list(p.get("items"))) {
            Map<String, Object> item = obj(row); JPanel line = new JPanel(new FlowLayout(FlowLayout.LEFT));
            String text = str(item.get("text")); line.add(new JLabel(str(item.get("kind")) + ": " + (text.length() > 100 ? text.substring(0, 100) + "…" : text)));
            line.add(Desktop.button("Edit / take back", () -> takeback(str(item.get("id")))));
            line.add(Desktop.button("Remove", () -> rpc("turn/unsteer", map("inputId", item.get("id")), v -> {}))); pending.add(line);
        }
        if (yes(p.get("paused"))) pending.add(Desktop.button("Queue paused · Resume", () -> rpc("queue/resume", Map.of(), v -> {})));
        pending.revalidate(); pending.repaint();
    }
    void chooseImages() {
        JFileChooser chooser = new JFileChooser(); chooser.setMultiSelectionEnabled(true);
        if (chooser.showOpenDialog(desktop.window) == JFileChooser.APPROVE_OPTION) attachFiles(Arrays.asList(chooser.getSelectedFiles()));
    }
    void attachFiles(List<File> files) {
        CompletableFuture.runAsync(() -> {
            for (File file : files) {
                try {
                    if (Files.size(file.toPath()) > 10 * 1024 * 1024) throw new IOException("Image exceeds 10 MiB: " + file);
                    byte[] bytes = Files.readAllBytes(file.toPath()); String mime = Files.probeContentType(file.toPath());
                    if (mime == null || !List.of("image/png", "image/jpeg", "image/gif", "image/webp").contains(mime)) throw new IOException("Unsupported image: " + file);
                    Object image = map("mimeType", mime, "data", Base64.getEncoder().encodeToString(bytes), "name", file.getName());
                    Desktop.edt(() -> { if (images.size() < 10) images.add(image); else desktop.error("At most 10 images per input"); updateAttachments(); });
                } catch (Exception e) { Desktop.edt(() -> desktop.error(e.getMessage())); }
            }
        });
    }
    void pasteImage() {
        try {
            Clipboard clip = Toolkit.getDefaultToolkit().getSystemClipboard();
            if (clip.isDataFlavorAvailable(DataFlavor.imageFlavor)) attachImage((Image)clip.getData(DataFlavor.imageFlavor));
        } catch (Exception e) { desktop.error(e.getMessage()); }
    }
    void attachImage(Image image) {
        CompletableFuture.runAsync(() -> {
            try {
                int w = image.getWidth(null), h = image.getHeight(null); if (w <= 0 || h <= 0 || (long)w * h > 64_000_000) throw new IOException("Invalid image dimensions");
                BufferedImage buffer = new BufferedImage(w, h, BufferedImage.TYPE_INT_ARGB); Graphics2D graphics = buffer.createGraphics(); graphics.drawImage(image, 0, 0, null); graphics.dispose();
                ByteArrayOutputStream bytes = new ByteArrayOutputStream(); ImageIO.write(buffer, "png", bytes);
                if (bytes.size() > 10 * 1024 * 1024) throw new IOException("Image exceeds 10 MiB");
                Object attached = map("mimeType", "image/png", "data", Base64.getEncoder().encodeToString(bytes.toByteArray()), "name", "pasted image");
                Desktop.edt(() -> { if (images.size() < 10) images.add(attached); updateAttachments(); });
            } catch (Exception e) { Desktop.edt(() -> desktop.error(e.getMessage())); }
        });
    }
    void updateAttachments() {
        attachments.removeAll(); for (Object image : images) attachments.add(Desktop.button(str(obj(image).getOrDefault("name", "Image")) + " ×", () -> { images.remove(image); updateAttachments(); }));
        attachments.revalidate(); attachments.repaint();
    }
    String token() {
        String before = composer.getText().substring(0, composer.getCaretPosition()); int space = Math.max(before.lastIndexOf(' '), before.lastIndexOf('\n')); return before.substring(space + 1);
    }
    void autoComplete() { String token = token(); if (token.equals("/") || token.equals("@")) complete(); }
    void complete() {
        String token = token();
        if (token.startsWith("/")) {
            List<Object> match = commands.stream().filter(c -> str(obj(c).get("name")).startsWith(token.substring(1))).toList();
            desktop.pick("Slash commands", match, c -> "/" + str(obj(c).get("name")) + " " + str(obj(c).get("args")) + " · " + str(obj(c).get("description")), c -> insertCompletion(token, "/" + str(obj(c).get("name"))));
        } else if (token.startsWith("@")) {
            rpc("thread/files", map("query", token.substring(1), "limit", 100), v -> desktop.pick("Workspace files", Json.list(obj(v).get("files")), c -> str(obj(c).get("path")) + (yes(obj(c).get("directory")) ? "/" : ""), c -> insertCompletion(token, "@" + str(obj(c).get("path")))));
        } else desktop.palette();
    }
    void insertCompletion(String old, String text) {
        int caret = composer.getCaretPosition(); String content = composer.getText(); int start = Math.max(0, caret - old.length());
        composer.replaceRange(text + " ", start, caret); composer.requestFocusInWindow();
    }
}
