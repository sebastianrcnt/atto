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
    final JTextField search = new Ui.Field("Search sessions");
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
        window.setSize(settings.integer("width", 1180), settings.integer("height", 820)); window.setMinimumSize(new Dimension(880, 620));
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
            pane.refreshCommands(); refreshSessions();
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
        left.setBorder(BorderFactory.createEmptyBorder(16, 12, 12, 12)); left.putClientProperty("atto.role", "surface"); left.setBackground(Ui.surface); left.add(search, BorderLayout.NORTH); JScrollPane sessionScroll = new JScrollPane(sidebar); sessionScroll.setBorder(BorderFactory.createEmptyBorder()); left.add(sessionScroll, BorderLayout.CENTER); sidebar.putClientProperty("atto.role", "surface"); sidebar.setBackground(Ui.surface); sidebar.setSelectionMode(ListSelectionModel.SINGLE_SELECTION);
        JPanel sessionButtons = new JPanel(new FlowLayout(FlowLayout.LEFT)); sessionButtons.setOpaque(false);
        sessionButtons.add(button("New", () -> newSession())); sessionButtons.add(button("Refresh", this::refreshSessions));
        left.add(sessionButtons, BorderLayout.SOUTH);
        sidebar.setCellRenderer((list, value, index, selected, focus) -> {
            Map<String, Object> session = obj(value);
            if (yes(session.get("projectHeader"))) {
                JLabel project = Ui.muted(str(session.get("cwd"))); project.setBorder(BorderFactory.createEmptyBorder(16, 8, 6, 8)); return project;
            }
            JPanel row = Ui.rounded(selected ? Ui.selected : Ui.surface, 12); row.setLayout(new BorderLayout(6, 5)); row.setBorder(BorderFactory.createEmptyBorder(10, 10, 10, 10));
            JLabel name = new JLabel("<html>" + Markdown.escape(sessionName(session)) + "</html>"); name.setFont(Ui.body(14).deriveFont(Font.BOLD)); row.add(name, BorderLayout.NORTH);
            row.add(Ui.muted(Ui.cwd(str(session.get("cwd")))), BorderLayout.CENTER);
            JPanel detail = new JPanel(new BorderLayout()); detail.setOpaque(false); detail.add(Ui.muted(Ui.relative(str(session.get("updatedAt")))));
            if (yes(session.get("busy"))) { JLabel busy = Ui.muted("● Working"); busy.setForeground(Ui.accent); detail.add(busy, BorderLayout.EAST); }
            row.add(detail, BorderLayout.SOUTH); return row;
        });
        sidebar.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { if (e.getClickCount() == 2) resumeSelected(); } });
        bind(sidebar, "ENTER", "resume", this::resumeSelected);
        split = new JSplitPane(JSplitPane.HORIZONTAL_SPLIT, left, tabs); split.setResizeWeight(0);
        split.setDividerLocation(settings.integer("sidebarWidth", 250)); split.setDividerSize(1); split.setBorder(BorderFactory.createEmptyBorder()); tabs.setUI(new Ui.Tabs());
        window.add(split, BorderLayout.CENTER);
        JToolBar toolbar = new JToolBar(); toolbar.setFloatable(false);
        toolbar.setBorder(BorderFactory.createEmptyBorder(8, 16, 8, 16));
        JLabel brand = new JLabel("atto"); brand.setFont(Ui.body(17).deriveFont(Font.BOLD)); toolbar.add(brand); toolbar.addSeparator(new Dimension(18, 1));
        toolbar.add(Ui.icon("plus", "New session · ⌘N", this::newSession));
        toolbar.add(Ui.icon("tree", "Session tree", () -> withPane(p -> Panels.tree(p, false))));
        toolbar.add(Ui.icon("jobs", "Background jobs", () -> withPane(Panels::jobs)));
        toolbar.add(Box.createHorizontalGlue()); connection.setForeground(Ui.muted); connection.setFont(Ui.body(12)); toolbar.add(connection);
        toolbar.addSeparator(new Dimension(12, 1)); toolbar.add(Ui.icon("search", "Command palette · ⌘K", this::palette)); window.add(toolbar, BorderLayout.NORTH);
        JMenuBar menu = new JMenuBar();
        JMenu file = new JMenu("Session"), tools = new JMenu("Actions"), view = new JMenu("View");
        for (var e : actions.entrySet()) {
            JMenu target = e.getKey().contains("session") || e.getKey().equals("Quit") ? file : e.getKey().contains("font") || e.getKey().contains("theme") ? view : tools;
            target.add(new JMenuItem(e.getValue()));
        }
        menu.add(file); menu.add(tools); menu.add(view); window.setJMenuBar(menu);
        startTimer(new javax.swing.Timer(10000, e -> {
            if (!settings.string("theme", "system").equals("system")) return;
            boolean before = systemDark; CompletableFuture.runAsync(Desktop::detectAppearance).thenRun(() -> edt(() -> { if (before != systemDark) { applyTheme("system"); refreshTheme(); } }));
        }));
        startTimer(new javax.swing.Timer(100, e -> sessions.values().forEach(p -> { if (yes(p.info.get("busy"))) p.status.repaint(); })));
        startTimer(new javax.swing.Timer(1000, e -> sessions.values().forEach(SessionPane::refreshStatus)));
        startTimer(new javax.swing.Timer(10000, e -> { if (core.protocol.ready) refreshSessions(); }));
    }
    void startTimer(javax.swing.Timer timer) { timers.add(timer); timer.start(); }
    void show() { refreshTheme(); window.setVisible(true); core.connect(); }
    void refreshTheme() { SwingUtilities.updateComponentTreeUI(window); Ui.restyle(window); for (Window owned : window.getOwnedWindows()) { SwingUtilities.updateComponentTreeUI(owned); Ui.restyle(owned); } sessions.values().forEach(p -> { p.transcript.rows.setBackground(Ui.canvas); p.transcript.invalidateRows(); }); }
    static void edt(Runnable task) { if (SwingUtilities.isEventDispatchThread()) task.run(); else SwingUtilities.invokeLater(task); }
    static JButton button(String text, Runnable action) { return Ui.button(text, action); }
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
        action("Create timer", 0, 0, () -> withPane(p -> input("Remind me when (e.g. 10m or 15:30)", "10m", when -> input("Reminder", "", message -> p.rpc("timer/create", map("when", when, "message", message), v -> {})))));
        action("Agents", KeyEvent.VK_A, InputEvent.SHIFT_DOWN_MASK, () -> withPane(p -> Panels.agents(p)));
        action("Context", KeyEvent.VK_I, 0, () -> withPane(p -> Panels.context(p, false)));
        action("System context", 0, 0, () -> withPane(p -> Panels.context(p, true)));
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
        action("Usage totals", 0, 0, () -> withPane(Panels::usage));
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
            settings.values.put("theme", theme); applyTheme(theme); refreshTheme(); persist();
        }));
        action("Authentication status", 0, 0, () -> withPane(Panels::authentication));
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
        core.hydrate(method, params).thenAccept(snapshot -> edt(() -> { SessionPane pane = sessions.get(str(snapshot.get("threadId"))); if (pane != null) tabs.setSelectedComponent(pane); })).exceptionally(e -> null);
    }
    void trust(SessionPane pane) {
        if (!pane.writable()) return;
        pane.rpc("thread/context", Map.of(), value -> {
            Map<String, Object> loaded = obj(obj(value).get("loaded")); pane.loadedContext = loaded;
            String key = str(pane.info.get("cwd")); String signature = write(map("hooks", loaded.get("hooks"), "extensions", loaded.get("extensions"), "mcp", loaded.get("mcp")));
            Map<String, Object> remembered = obj(settings.values.get("startupTrust"));
            if (signature.equals(remembered.get(key))) { pane.rpc("thread/sessionStart", Map.of(), x -> {}); return; }
            JPanel content = new JPanel(new BorderLayout(0, 16)); content.setBorder(BorderFactory.createEmptyBorder(24, 24, 20, 24));
            JLabel heading = new JLabel("Allow atto to work in this project?"); heading.setFont(Ui.body(20).deriveFont(Font.BOLD)); content.add(heading, BorderLayout.NORTH);
            JPanel details = new JPanel(); details.setLayout(new BoxLayout(details, BoxLayout.Y_AXIS));
            details.add(new JLabel("<html>Session startup can run configured hooks, extensions and MCP servers.<br>Only content approved by the runtime is enabled. Review the workspace below.</html>"));
            details.add(Box.createVerticalStrut(16)); details.add(Ui.muted(Ui.cwd(key))); details.add(Box.createVerticalStrut(12));
            for (String kind : List.of("hooks", "extensions", "mcp")) {
                List<Object> entries = Json.list(loaded.get(kind));
                JLabel section = new JLabel(switch (kind) { case "hooks" -> "Hooks"; case "extensions" -> "Extensions"; default -> "MCP servers"; } + " · " + entries.size()); section.setFont(Ui.body(14).deriveFont(Font.BOLD)); details.add(section);
                if (entries.isEmpty()) details.add(Ui.muted("None configured"));
                for (Object entry : entries) { Map<String, Object> item = obj(entry);
                    String name = str(item.getOrDefault("name", item.getOrDefault("event", "")));
                    String target = str(item.getOrDefault("command", item.getOrDefault("target", item.getOrDefault("path", ""))));
                    details.add(new JLabel("<html>• " + Markdown.escape(name) + " <span style='color:" + Transcript.color(Ui.muted) + "'>" + Markdown.escape(Ui.cwd(target)) + " · " + Markdown.escape(str(item.get("status"))) + "</span></html>"));
                }
                details.add(Box.createVerticalStrut(12));
            }
            details.add(Ui.muted("Unapproved project content stays disabled. Use atto trust to approve it.")); content.add(details);
            JDialog dialog = dialog("Project trust", content); JPanel buttons = new JPanel(new FlowLayout(FlowLayout.RIGHT, 8, 12));
            Runnable allow = () -> { dialog.dispose(); pane.rpc("thread/sessionStart", Map.of(), x -> {}); };
            buttons.add(button("Detach", () -> { dialog.dispose(); detach(pane, false); })); buttons.add(button("Allow once", allow));
            JButton remember = button("Allow", () -> { Map<String, Object> trust = new LinkedHashMap<>(obj(settings.values.get("startupTrust"))); trust.put(key, signature); settings.values.put("startupTrust", trust); persist(); allow.run(); }); remember.putClientProperty("primary", true); buttons.add(remember);
            buttons.setBorder(BorderFactory.createEmptyBorder(0, 16, 4, 16)); dialog.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setLocationRelativeTo(window); dialog.setVisible(true);
        });
    }
    static String sessionName(Map<String, Object> session) {
        String name = str(session.get("name")); if (!name.isEmpty()) return name; String preview = str(session.get("preview")); if (!preview.isEmpty()) return preview.length() > 32 ? preview.substring(0, 32) + "…" : preview;
        for (Object value : Json.list(session.get("items"))) if ("userMessage".equals(obj(value).get("type"))) {
            String prompt = str(obj(value).get("text")).replace('\n', ' '); if (!prompt.isEmpty()) return prompt.length() > 32 ? prompt.substring(0, 32) + "…" : prompt;
        }
        return "New conversation";
    }
    void update(Map<String, Object> snapshot) {
        if (closing) return;
        String id = str(snapshot.get("threadId")); if (id.isEmpty()) return;
        SessionPane pane = sessions.get(id);
        boolean namesChanged = pane == null || !str(pane.info.get("name")).equals(str(snapshot.get("name")));
        if (pane == null) { pane = new SessionPane(this, id); sessions.put(id, pane); tabs.addTab(id, pane); tabs.setSelectedComponent(pane); }
        pane.update(snapshot);
        int index = tabs.indexOfComponent(pane); String name = str(snapshot.get("name"));
        String title = sessionName(snapshot); tabs.setTitleAt(index, title);
        JPanel tab = new JPanel(new FlowLayout(FlowLayout.LEFT, 6, 0)); tab.setOpaque(false); JLabel label = new JLabel((yes(snapshot.get("busy")) ? "● " : "") + title); label.setFont(Ui.body(13)); tab.add(label);
        SessionPane current = pane; JButton close = Ui.icon("close", "Detach session", () -> detach(current, false)); close.setBorder(BorderFactory.createEmptyBorder(3, 3, 3, 3)); tab.add(close); tabs.setTabComponentAt(index, tab); if (namesChanged) refreshSessions();
    }
    void refreshSessions() {
        core.call("thread/list", "", Map.of()).whenComplete((v, e) -> edt(() -> {
            if (e != null) { connection.setText(e.getMessage()); return; }
            allSessions = Json.list(obj(v).get("threads")); filterSessions();
        }));
    }
    void filterSessions() {
        String active = tabs.getSelectedComponent() instanceof SessionPane selectedPane ? selectedPane.id : sidebar.getSelectedValue() == null ? "" : str(sidebar.getSelectedValue().get("threadId"));
        String query = search.getText().toLowerCase(Locale.ROOT); sessionList.clear();
        Map<String, List<Map<String, Object>>> projects = new LinkedHashMap<>();
        for (Object row : allSessions) if (write(row).toLowerCase(Locale.ROOT).contains(query)) projects.computeIfAbsent(str(obj(row).get("cwd")), key -> new ArrayList<>()).add(obj(row));
        for (var project : projects.entrySet()) {
            sessionList.addElement(map("projectHeader", true, "cwd", Ui.cwd(project.getKey())));
            for (var saved : project.getValue()) { Map<String, Object> session = new LinkedHashMap<>(saved); SessionPane live = sessions.get(str(session.get("threadId"))); if (live != null) { session.put("busy", live.info.get("busy")); if (str(session.get("name")).isEmpty()) session.put("name", sessionName(live.info)); } sessionList.addElement(session); if (active.equals(str(session.get("threadId")))) sidebar.setSelectedIndex(sessionList.size() - 1); }
        }
    }
    void resumeSelected() { if (sidebar.getSelectedValue() != null && !yes(sidebar.getSelectedValue().get("projectHeader"))) open("thread/resume", map("threadId", sidebar.getSelectedValue().get("threadId"), "deferStart", true)); }
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
                }))); buttons.add(button("Copy URL", () -> copy(url))); dialog.add(buttons, BorderLayout.SOUTH); showDialog(dialog);
            }
            if (p.containsKey("error")) error(str(p.get("error")));
        }
        if (method.equals("extension/notify")) {
            connection.setToolTipText(str(p.get("message"))); attention(str(p.get("message")), id);
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
        if (old != null) { old.dispose(); prompts.remove(pane.id); } if (promptId.isEmpty()) return;
        JPanel body = new JPanel(new BorderLayout(0, 16)); body.setPreferredSize(new Dimension(500, yes(prompt.get("confirm")) ? 130 : 240));
        JPanel header = new JPanel(new BorderLayout(0, 8)); JLabel title = new JLabel("<html>" + Markdown.escape(str(prompt.get("title"))) + "</html>"); title.setFont(Ui.body(18).deriveFont(Font.BOLD)); header.add(title);
        header.add(Ui.muted(str(prompt.get("subtitle")) + " " + str(prompt.get("note"))), BorderLayout.SOUTH); body.add(header, BorderLayout.NORTH);
        JTextField input = new Ui.Field(str(prompt.getOrDefault("placeholder", "Enter your answer…"))); input.setText(str(prompt.get("text")));
        List<Object> options = Json.list(prompt.get("options")); JList<String> choices = new JList<>(options.stream().map(x -> str(obj(x).get("label")) + " " + str(obj(x).get("description"))).toArray(String[]::new)); choices.setFixedCellHeight(36);
        boolean multi = "multiSelect".equals(prompt.get("kind")), select = multi || "select".equals(prompt.get("kind"));
        choices.setSelectionMode(multi ? ListSelectionModel.MULTIPLE_INTERVAL_SELECTION : ListSelectionModel.SINGLE_SELECTION); choices.setSelectedIndex((int)num(prompt.get("selected")));
        if (!yes(prompt.get("confirm"))) body.add(select ? new JScrollPane(choices) : input, BorderLayout.CENTER);
        JDialog dialog = dialog("atto · " + str(prompt.get("origin")), body); dialog.getRootPane().putClientProperty("promptId", promptId); prompts.put(pane.id, dialog);
        JPanel buttons = new JPanel(new FlowLayout(FlowLayout.RIGHT, 8, 0));
        Runnable answer = () -> {
            Map<String, Object> params = map("id", promptId);
            if (multi) params.put("indexes", Arrays.stream(choices.getSelectedIndices()).boxed().toList());
            else if (select) { if (choices.getSelectedIndex() < 0) return; params.put("index", choices.getSelectedIndex()); }
            else params.put("text", input.getText()); pane.rpc("prompt/answer", params, v -> {});
        };
        if (yes(prompt.get("confirm"))) {
            for (int i = options.size() - 1; i >= 0; i--) { int index = i; JButton choice = button(str(obj(options.get(i)).get("label")), () -> pane.rpc("prompt/answer", map("id", promptId, "index", index), v -> {})); if (i == 0) choice.putClientProperty("primary", true); buttons.add(choice); }
        } else {
            buttons.add(button("Cancel", () -> pane.rpc("prompt/answer", map("id", promptId, "cancel", true), v -> {}))); JButton submit = button(select ? "Choose" : "Continue", answer); submit.putClientProperty("primary", true); buttons.add(submit);
        }
        dialog.addWindowListener(new WindowAdapter() { public void windowClosing(WindowEvent e) { pane.rpc("prompt/answer", map("id", promptId, "cancel", true), v -> {}); } });
        bind(body, "ENTER", "answer", answer); body.add(buttons, BorderLayout.SOUTH); dialog.pack(); dialog.setLocationRelativeTo(window); dialog.setVisible(true); if (!select) input.requestFocusInWindow(); attention("Prompt waiting", pane.id);
    }
    JDialog dialog(String title, Component body) {
        JDialog dialog = new JDialog(window, title, false); dialog.setDefaultCloseOperation(WindowConstants.DISPOSE_ON_CLOSE);
        JPanel padded = new JPanel(new BorderLayout()); padded.setBorder(BorderFactory.createEmptyBorder(16, 16, 16, 16)); padded.add(body); dialog.add(padded, BorderLayout.CENTER); dialog.pack(); dialog.setLocationRelativeTo(window); Ui.restyle(dialog); return dialog;
    }
    void showDialog(JDialog dialog) {
        dialog.pack(); dialog.setSize(Math.min(dialog.getWidth(), window.getWidth() - 64), Math.min(dialog.getHeight(), window.getHeight() - 80)); dialog.setLocationRelativeTo(window); dialog.setVisible(true);
    }
    void input(String title, String initial, Consumer<String> answer) {
        JTextField field = new JTextField(initial, 45); JDialog dialog = dialog(title, field);
        Runnable submit = () -> { String text = field.getText(); dialog.dispose(); answer.accept(text); };
        dialog.add(button("OK", submit), BorderLayout.SOUTH); field.addActionListener(e -> submit.run());
        showDialog(dialog); field.requestFocusInWindow();
    }
    void notice(String title, String message) {
        JPanel body = new JPanel(new BorderLayout(0, 16)); body.setPreferredSize(new Dimension(530, 150)); JLabel heading = new JLabel(title); heading.setFont(Ui.body(18).deriveFont(Font.BOLD)); body.add(heading, BorderLayout.NORTH);
        body.add(new JLabel("<html>" + Markdown.escape(message).replace("\n", "<br>") + "</html>")); JDialog dialog = dialog(title, body); JPanel controls = new JPanel(new FlowLayout(FlowLayout.RIGHT)); controls.add(button("Done", dialog::dispose)); body.add(controls, BorderLayout.SOUTH); showDialog(dialog);
    }
    void text(String title, String value, Runnable extra) {
        JTextArea area = new JTextArea(value, 25, 85); area.setEditable(false); area.setFont(new Font(Font.MONOSPACED, Font.PLAIN, fontSize));
        JDialog dialog = dialog(title, new JScrollPane(area)); JPanel buttons = new JPanel();
        buttons.add(button("Copy", () -> copy(value))); buttons.add(button("Save…", () -> save("atto.txt", value)));
        if (extra != null) buttons.add(button("Refresh", extra)); dialog.add(buttons, BorderLayout.SOUTH); showDialog(dialog);
    }
    <T> void pick(String title, List<T> entries, java.util.function.Function<T, String> label, Consumer<T> select) {
        JTextField query = new Ui.Field("Search…"); DefaultListModel<T> model = new DefaultListModel<>(); JList<T> list = new JList<>(model);
        list.setSelectionMode(ListSelectionModel.SINGLE_SELECTION);
        list.setCellRenderer(new DefaultListCellRenderer() { public Component getListCellRendererComponent(JList<?> l, Object v, int i, boolean selected, boolean focus) {
            @SuppressWarnings("unchecked") T entry = (T)v;
            JLabel cell = (JLabel)super.getListCellRendererComponent(l, label.apply(entry), i, selected, focus); cell.setBorder(BorderFactory.createEmptyBorder(10, 12, 10, 12)); return cell;
        } });
        Runnable filter = () -> { model.clear(); for (T entry : entries) if (label.apply(entry).toLowerCase(Locale.ROOT).contains(query.getText().toLowerCase(Locale.ROOT))) model.addElement(entry); if (!model.isEmpty()) list.setSelectedIndex(0); };
        query.getDocument().addDocumentListener(listener(filter)); filter.run();
        JPanel body = new JPanel(new BorderLayout(0, 12)); body.add(query, BorderLayout.NORTH); body.add(new JScrollPane(list), BorderLayout.CENTER); body.setPreferredSize(new Dimension(700, 420));
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
        dialog.add(button("Select", choose), BorderLayout.SOUTH); showDialog(dialog); query.requestFocusInWindow();
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
        sessions.values().forEach(p -> { p.composer.setFont(Ui.body(fontSize)); p.transcript.invalidateRows(); }); persist();
    }
    void applyTheme(String theme) { Ui.theme(theme.equals("system") ? systemDark : theme.equals("dark")); }
    static volatile boolean systemDark;
    static void detectAppearance() {
        if (!System.getProperty("os.name").startsWith("Mac")) { Color bg = UIManager.getColor("Panel.background"); systemDark = bg != null && bg.getRed() < 100; return; }
        try { Process p = new ProcessBuilder("defaults", "read", "-g", "AppleInterfaceStyle").start(); systemDark = new String(p.getInputStream().readAllBytes()).trim().equalsIgnoreCase("Dark"); p.waitFor(); }
        catch (Exception ignored) { systemDark = false; }
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
    final JTextArea composer = new Ui.Composer();
    JButton send; final JButton modelChip, effortChip; final JProgressBar contextMeter = new JProgressBar(0, 100);
    final JPanel pending = new JPanel(), attachments = new JPanel();
    final JLabel status = new JLabel(" "), customStatus = new JLabel(" "), extensions = new JLabel(" ");
    String statusFingerprint = "";
    boolean statusPending;
    long statusRefreshAt;
    int statusRefreshInterval;
    final JTextArea goal = new JTextArea();
    final List<Object> images = new ArrayList<>();
    final List<JButton> writeButtons = new ArrayList<>();
    final JPopupMenu completionPopup = new JPopupMenu();
    final DefaultListModel<String> completionEntries = new DefaultListModel<>(); final JList<String> completionList = new JList<>(completionEntries);
    String completionToken = ""; long completionGeneration;
    Map<String, Object> loadedContext = Map.of();
    Map<String, Object> info = Map.of(); List<Object> commands = List.of();
    boolean trustPending = true;
    SessionPane(Desktop desktop, String id) {
        super(new BorderLayout(6, 6)); this.desktop = desktop; this.id = id;
        transcript = new Transcript(this); add(transcript, BorderLayout.CENTER);
        composer.setLineWrap(true); composer.setWrapStyleWord(true); composer.setFont(Ui.body(desktop.fontSize)); composer.setBorder(BorderFactory.createEmptyBorder(4, 4, 4, 4));
        pending.setLayout(new BoxLayout(pending, BoxLayout.Y_AXIS)); attachments.setLayout(new FlowLayout(FlowLayout.LEFT));
        JPanel bottom = new JPanel(new BorderLayout(0, 8)); bottom.setBorder(BorderFactory.createEmptyBorder(8, 24, 12, 24));
        JPanel editor = Ui.rounded(Ui.surface, 20); editor.setLayout(new BorderLayout(0, 8)); editor.setBorder(BorderFactory.createEmptyBorder(12, 14, 10, 14));
        composer.setOpaque(false); JScrollPane inputScroll = new JScrollPane(composer); inputScroll.setBorder(BorderFactory.createEmptyBorder()); inputScroll.setOpaque(false); inputScroll.getViewport().setOpaque(false); editor.add(inputScroll);
        JPanel controls = new JPanel(new BorderLayout()); controls.setOpaque(false); JPanel tools = new JPanel(new FlowLayout(FlowLayout.LEFT, 4, 0)); tools.setOpaque(false);
        tools.add(Ui.icon("attach", "Attach image", this::chooseImages));
        JButton more = Ui.icon("more", "Input actions", () -> {}); JPopupMenu choices = new JPopupMenu();
        for (var entry : List.of(new Object[]{"Queue · Tab", (Runnable)() -> submit("queue")}, new Object[]{"Send now · ⌘↵", (Runnable)() -> submit("replace")}, new Object[]{"Hard cancel", (Runnable)() -> interrupt(true)}, new Object[]{"Complete · Ctrl+Space", (Runnable)this::complete}, new Object[]{"Paste image", (Runnable)this::pasteImage})) {
            JMenuItem choice = new JMenuItem((String)entry[0]); choice.addActionListener(e -> ((Runnable)entry[1]).run()); choices.add(choice);
        }
        more.addActionListener(e -> choices.show(more, 0, more.getHeight())); tools.add(more); controls.add(tools, BorderLayout.WEST);
        controls.add(Ui.muted("Enter to send · Shift+Enter for a new line"), BorderLayout.CENTER);
        send = Desktop.button("Send", () -> { if (yes(info.get("busy"))) interrupt(false); else submit("auto"); }); send.setIcon(new Ui.VectorIcon("send", 16)); send.putClientProperty("primary", true); writeButtons.add(send); controls.add(send, BorderLayout.EAST); editor.add(controls, BorderLayout.SOUTH);
        JPanel north = new JPanel(new BorderLayout()); north.add(pending); north.add(attachments, BorderLayout.SOUTH);
        bottom.add(north, BorderLayout.NORTH); bottom.add(editor);
        JPanel foot = new JPanel(new BorderLayout(12, 4)); JPanel chips = new JPanel(new FlowLayout(FlowLayout.LEFT, 6, 0));
        modelChip = Desktop.button("Model", () -> Panels.models(this)); effortChip = Desktop.button("Effort", () -> Panels.effort(this)); chips.add(modelChip); chips.add(effortChip);
        contextMeter.setPreferredSize(new Dimension(54, 5)); contextMeter.setBorderPainted(false); chips.add(contextMeter); foot.add(chips, BorderLayout.WEST);
        status.putClientProperty("atto.role", "muted"); status.setFont(Ui.body(11)); status.setForeground(Ui.muted); foot.add(status);
        JPanel custom = new JPanel(new GridLayout(0, 1)); custom.add(customStatus); custom.add(extensions); foot.add(custom, BorderLayout.SOUTH); customStatus.setVisible(false); extensions.setVisible(false);
        bottom.add(foot, BorderLayout.SOUTH); add(bottom, BorderLayout.SOUTH);
        goal.setEditable(false); goal.setLineWrap(true); goal.setWrapStyleWord(true); goal.setRows(2); goal.setFont(Ui.body(13)); goal.setForeground(Ui.muted); goal.setBorder(BorderFactory.createEmptyBorder(10, 24, 8, 24)); goal.setVisible(false); add(goal, BorderLayout.NORTH);
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
        info = snapshot; if (!obj(snapshot.get("context")).isEmpty()) loadedContext = obj(snapshot.get("context")); transcript.update(Json.list(snapshot.get("items"))); updatePending(); updateControls(); refreshStatus();
        Map<String, Object> g = obj(info.get("goal")); goal.setVisible(!g.isEmpty());
        goal.setText("Goal: " + str(g.get("objective")) + "\n" + str(g.get("statusLabel")) + " · " + str(g.get("tokensUsed")) + " tokens · " + str(g.get("note")));
        desktop.prompt(this, obj(info.get("prompt")));
    }
    boolean writable() { return desktop.core.protocol.ready && str(info.get("readOnly")).isEmpty() && !yes(info.get("offline")) && !yes(info.get("closed")); }
    void updateControls() { composer.setEditable(writable()); writeButtons.forEach(b -> b.setEnabled(writable()));
        ((Ui.Composer)composer).placeholder = writable() ? "Ask atto… / for commands, @ for files, ! for shell" : desktop.core.protocol.ready ? "Read-only session · messages cannot be sent" : "Disconnected · reconnecting…";
        composer.repaint(); modelChip.setEnabled(writable()); effortChip.setEnabled(writable());
        send.setText(yes(info.get("busy")) ? "Stop" : "Send"); send.setIcon(new Ui.VectorIcon(yes(info.get("busy")) ? "stop" : "send", 16));
    }
    void refreshStatus() {
        Map<String, Object> usage = obj(info.get("usage")), activity = obj(info.get("activity"));
        long context = num(info.get("contextTokens")), window = num(info.get("contextWindow")), input = num(usage.get("inputTokens")), cached = num(usage.get("cachedInputTokens"));
        long started = num(activity.get("startedAt")); if (started == 0) started = num(obj(info.get("turn")).get("startedAt"));
        boolean shellRunning = Json.list(info.get("items")).stream().anyMatch(x -> yes(obj(x).get("shell")) && "inProgress".equals(obj(x).get("status")));
        String busy = yes(info.get("busy")) ? str(activity.getOrDefault("phase", "Working")) + " " + (started > 0 ? (System.currentTimeMillis() - started) / 1000 : 0) + "s" : shellRunning ? "Shell running" : "Idle";
        modelChip.setText(str(info.getOrDefault("modelName", info.getOrDefault("model", "Choose model"))) + " ▾"); effortChip.setText(str(info.getOrDefault("effort", "Effort")) + " ▾");
        effortChip.setVisible(!Json.list(info.get("efforts")).isEmpty());
        int percent = window > 0 ? (int)Math.min(100, context * 100 / window) : 0; contextMeter.setValue(percent); contextMeter.setToolTipText(context + " / " + window + " context tokens · " + percent + "%");
        double cost = 0; try { cost = Double.parseDouble(str(usage.getOrDefault("cost", 0))); } catch (NumberFormatException ignored) {}
        status.setIcon(yes(info.get("busy")) ? new Ui.Spinner(13) : null);
        status.setText((yes(info.get("busy")) ? busy + "  ·  " : "") + percent + "% context · " + (input > 0 ? cached * 100 / input : 0) + "% cache · " + compact(input) + " ↑ " + compact(num(usage.get("outputTokens"))) + " ↓ · " + String.format(Locale.ROOT, "$%.3f", cost) + " · " + Ui.cwd(str(info.get("cwd"))) + (num(info.get("jobs")) > 0 ? " · " + num(info.get("jobs")) + " jobs" : "") + (num(info.get("timers")) > 0 ? " · " + num(info.get("timers")) + " timers" : "") + (writable() ? "" : " · Read only"));
        status.setToolTipText("Jobs: " + num(info.get("jobs")) + " · Timers: " + num(info.get("timers")) + " · " + str(info.get("cwd")));
        Map<String, Object> ui = obj(info.get("extensionUi")); List<String> lines = new ArrayList<>();
        for (Object s : Json.list(ui.get("status"))) lines.add(str(obj(s).get("text")));
        for (Object w : Json.list(ui.get("widgets"))) for (Object line : Json.list(obj(w).get("lines"))) lines.add(str(line));
        extensions.setVisible(!lines.isEmpty()); extensions.setText("<html>" + Markdown.escape(String.join("\n", lines)).replace("\n", "<br>") + "</html>");
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
    static String compact(long n) { return n < 1000 ? Long.toString(n) : String.format(Locale.ROOT, "%.1fk", n / 1000.0); }
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
        if (completionPopup.isVisible() && intent.equals("auto")) { chooseCompletion(); return; }
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
            case "context" -> { if (!arg.isEmpty() && !arg.equals("system")) return false; Panels.context(this, !arg.isEmpty()); }
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
        pending.removeAll(); pending.setLayout(new FlowLayout(FlowLayout.LEFT, 6, 4)); Map<String, Object> p = obj(info.get("pending"));
        for (Object row : Json.list(p.get("items"))) {
            Map<String, Object> item = obj(row); JPanel chip = Ui.rounded(Ui.selected, 12); chip.setLayout(new FlowLayout(FlowLayout.LEFT, 6, 0)); chip.setBorder(BorderFactory.createEmptyBorder(4, 8, 4, 4));
            String text = str(item.get("text")); JLabel label = new JLabel(str(item.get("kind")) + " · " + (text.length() > 55 ? text.substring(0, 55) + "…" : text)); label.setFont(Ui.body(12)); label.setCursor(Cursor.getPredefinedCursor(Cursor.HAND_CURSOR));
            label.setToolTipText("Click to edit / take back"); label.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { takeback(str(item.get("id"))); } }); chip.add(label);
            chip.add(Ui.icon("close", "Remove pending message", () -> rpc("turn/unsteer", map("inputId", item.get("id")), v -> {}))); pending.add(chip);
        }
        if (yes(p.get("paused"))) pending.add(Desktop.button("Queue paused · Resume", () -> rpc("queue/resume", Map.of(), v -> {})));
        pending.setVisible(pending.getComponentCount() > 0); pending.revalidate(); pending.repaint();
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
    void autoComplete() {
        String token = token(); if (token.startsWith("/") && !composer.getText().substring(0, composer.getCaretPosition()).contains(" ") || token.startsWith("@")) complete(); else completionPopup.setVisible(false);
    }
    void complete() {
        String token = token(); completionToken = token; long generation = ++completionGeneration;
        if (token.startsWith("/")) {
            List<String> matches = commands.stream().filter(c -> str(obj(c).get("name")).startsWith(token.substring(1))).map(c -> "/" + str(obj(c).get("name")) + "  ·  " + str(obj(c).get("description"))).toList();
            showCompletions(matches);
        } else if (token.startsWith("@")) {
            rpc("thread/files", map("query", token.substring(1), "limit", 40), v -> { if (generation == completionGeneration && token.equals(token())) showCompletions(Json.list(obj(v).get("files")).stream().map(c -> "@" + str(obj(c).get("path"))).toList()); });
        } else desktop.palette();
    }
    void showCompletions(List<String> entries) {
        completionEntries.clear(); entries.forEach(completionEntries::addElement); if (entries.isEmpty()) { completionPopup.setVisible(false); return; }
        completionList.setSelectedIndex(0); completionList.setFont(Ui.body(13)); completionList.setFixedCellHeight(32);
        if (completionPopup.getComponentCount() == 0) {
            completionPopup.setFocusable(false); JScrollPane scroll = new JScrollPane(completionList); scroll.setBorder(BorderFactory.createEmptyBorder()); scroll.setPreferredSize(new Dimension(500, 192)); completionPopup.add(scroll);
            completionList.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { chooseCompletion(); } });
            for (String direction : List.of("UP", "DOWN")) { composer.getInputMap().put(KeyStroke.getKeyStroke(direction), "complete-" + direction); composer.getActionMap().put("complete-" + direction, new AbstractAction() { public void actionPerformed(ActionEvent e) {
                if (completionPopup.isVisible()) { int index = Math.max(0, Math.min(completionEntries.size() - 1, completionList.getSelectedIndex() + (direction.equals("DOWN") ? 1 : -1))); completionList.setSelectedIndex(index); completionList.ensureIndexIsVisible(index); }
                else { Action original = composer.getActionMap().get(direction.equals("DOWN") ? "caret-down" : "caret-up"); if (original != null) original.actionPerformed(e); }
            } }); }
            composer.getInputMap().put(KeyStroke.getKeyStroke("ESCAPE"), "completion-close"); composer.getActionMap().put("completion-close", new AbstractAction() { public void actionPerformed(ActionEvent e) { if (completionPopup.isVisible()) completionPopup.setVisible(false); else interrupt(false); } });
        }
        Ui.restyle(completionPopup); ((JScrollPane)completionPopup.getComponent(0)).setPreferredSize(new Dimension(Math.min(500, composer.getWidth()), Math.min(192, entries.size() * 32))); completionPopup.pack();
        if (composer.isShowing()) { completionPopup.show(composer, 0, -Math.min(200, entries.size() * 32 + 8)); composer.requestFocusInWindow(); }
    }
    void chooseCompletion() {
        String chosen = completionList.getSelectedValue(); completionPopup.setVisible(false); if (chosen != null) insertCompletion(completionToken, chosen.split("  ·  ", 2)[0]);
    }
    void insertCompletion(String old, String text) {
        int caret = composer.getCaretPosition(); String content = composer.getText(); int start = Math.max(0, caret - old.length());
        composer.replaceRange(text + " ", start, caret); composer.requestFocusInWindow();
    }
}
