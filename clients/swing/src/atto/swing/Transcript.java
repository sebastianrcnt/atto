package atto.swing;

import javax.swing.*;
import javax.swing.event.HyperlinkEvent;
import java.awt.*;
import java.awt.event.*;
import java.net.URI;
import java.util.*;
import java.util.List;
import java.util.concurrent.*;
import javax.imageio.ImageIO;
import java.io.ByteArrayInputStream;
import static atto.swing.Json.*;

/** Keeps a bounded page of components; coalesces stream updates at 20fps. */
final class Transcript extends JPanel {
    final SessionPane pane;
    final JPanel rows = new Rows();
    static final class Rows extends JPanel implements Scrollable {
        public Dimension getPreferredScrollableViewportSize() { return new Dimension(800, 600); }
        public int getScrollableUnitIncrement(Rectangle visible, int orientation, int direction) { return 22; }
        public int getScrollableBlockIncrement(Rectangle visible, int orientation, int direction) { return Math.max(22, visible.height - 22); }
        public boolean getScrollableTracksViewportWidth() { return true; }
        public boolean getScrollableTracksViewportHeight() { return false; }
    }
    final JScrollPane scroll = new JScrollPane(rows);
    final JButton bottom = Desktop.button("Jump to bottom ↓", this::bottom);
    final JButton earlier = Desktop.button("Load earlier messages", this::loadEarlier);
    final Map<String, JPanel> rendered = new LinkedHashMap<>();
    final Map<String, String> fingerprints = new HashMap<>();
    final Map<String, ImageIcon> imageCache = new LinkedHashMap<>(128, .75f, true) {
        protected boolean removeEldestEntry(Map.Entry<String, ImageIcon> entry) { return size() > 128; }
    };
    final Set<String> loadingImages = new HashSet<>(), expanded = new HashSet<>();
    final javax.swing.Timer timer;
    List<Object> items = List.of();
    int page = 200;
    boolean dirty, following = true, adjusting;
    int userScrollVersion;
    Transcript(SessionPane pane) {
        super(new BorderLayout()); this.pane = pane;
        rows.setLayout(new BoxLayout(rows, BoxLayout.Y_AXIS));
        rows.setBackground(Ui.canvas); scroll.setBorder(BorderFactory.createEmptyBorder()); scroll.getViewport().setBackground(Ui.canvas); scroll.getVerticalScrollBar().setUnitIncrement(22);
        scroll.addMouseWheelListener(e -> { if (e.getWheelRotation() < 0) { following = false; userScrollVersion++; bottom.setVisible(true); } });
        scroll.getVerticalScrollBar().addMouseListener(new MouseAdapter() { public void mousePressed(MouseEvent e) { following = false; userScrollVersion++; bottom.setVisible(true); } });
        scroll.getVerticalScrollBar().addAdjustmentListener(e -> {
            if (adjusting || !scroll.getVerticalScrollBar().getValueIsAdjusting()) return;
            JScrollBar bar = scroll.getVerticalScrollBar();
            following = bar.getValue() + bar.getVisibleAmount() >= bar.getMaximum() - 35;
            bottom.setVisible(!following);
        });
        add(earlier, BorderLayout.NORTH); add(scroll, BorderLayout.CENTER); add(bottom, BorderLayout.SOUTH); bottom.setVisible(false);
        scroll.addComponentListener(new ComponentAdapter() { public void componentResized(ComponentEvent e) { invalidateRows(); } });
        timer = new javax.swing.Timer(50, e -> { if (dirty) render(); }); timer.start();
    }
    void update(List<Object> items) { this.items = items; dirty = true; }
    void invalidateRows() { fingerprints.clear(); dirty = true; }
    void close() { timer.stop(); }
    void loadEarlier() { following = false; userScrollVersion++; page += 200; fingerprints.clear(); dirty = true; }
    void bottom() { following = true; bottom.setVisible(false); SwingUtilities.invokeLater(() -> scroll.getVerticalScrollBar().setValue(scroll.getVerticalScrollBar().getMaximum())); }
    void render() {
        dirty = false; int scrollVersion = userScrollVersion; boolean follow = following; int oldScroll = scroll.getVerticalScrollBar().getValue(); adjusting = true;
        int start = Math.max(0, items.size() - page); earlier.setVisible(start > 0);
        List<String> ids = new ArrayList<>(); boolean structure = false;
        for (Object value : items.subList(start, items.size())) {
            Map<String, Object> item = obj(value); String id = str(item.get("id")); ids.add(id);
            String fingerprint = write(item) + "|" + pane.desktop.fontSize + "|" + expanded.contains(id);
            JPanel row = rendered.get(id);
            if (row == null) { row = new JPanel(new BorderLayout()); rendered.put(id, row); structure = true; }
            if (!fingerprint.equals(fingerprints.get(id))) { row.removeAll(); fill(row, item); fingerprints.put(id, fingerprint); row.revalidate(); row.repaint(); }
        }
        if (!new ArrayList<>(rendered.keySet()).equals(ids)) structure = true;
        if (structure) {
            rows.removeAll(); Map<String, JPanel> ordered = new LinkedHashMap<>();
            for (String id : ids) { JPanel row = rendered.get(id); ordered.put(id, row); rows.add(row); }
            rendered.clear(); rendered.putAll(ordered); fingerprints.keySet().retainAll(rendered.keySet());
        }
        rows.revalidate(); rows.repaint();
        SwingUtilities.invokeLater(() -> {
            if (scrollVersion != userScrollVersion) { adjusting = false; return; }
            scroll.getVerticalScrollBar().setValue(follow ? scroll.getVerticalScrollBar().getMaximum() : oldScroll);
            adjusting = false; following = follow;
        });
    }
    void fill(JPanel row, Map<String, Object> item) {
        String id = str(item.get("id")), kind = str(item.get("type")); boolean open = expanded.contains(id);
        int margin = Math.max(28, (scroll.getViewport().getWidth() - 790) / 2);
        row.setBackground(Ui.canvas); row.setBorder(BorderFactory.createEmptyBorder(8, margin, 8, margin));
        row.setAlignmentX(Component.LEFT_ALIGNMENT);
        String entry = str(item.get("entryId")), title = str(item.get("title"));
        String text = str(obj(item.get("display")).getOrDefault("text", item.getOrDefault("text", "")));
        String copyText = text; JPopupMenu menu = new JPopupMenu();
        menu.add(new JMenuItem(new AbstractAction("Copy") { public void actionPerformed(ActionEvent e) { Desktop.copy("commandExecution".equals(kind) ? commandOutput(item) : copyText); } }));
        if (!entry.isEmpty()) {
            menu.addSeparator();
            menu.add(new JMenuItem(new AbstractAction("Fork from this message") { public void actionPerformed(ActionEvent e) { Panels.fork(pane, entry); } }));
            menu.add(new JMenuItem(new AbstractAction("Label this entry") { public void actionPerformed(ActionEvent e) { pane.desktop.input("Entry label", "", label -> pane.rpc("thread/setLabel", map("entryId", entry, "label", label), v -> {})); } }));
        }
        JPanel body = new JPanel(); body.setOpaque(false); body.setLayout(new BoxLayout(body, BoxLayout.Y_AXIS));
        if (kind.equals("userMessage")) {
            JPanel bubble = Ui.rounded(Ui.surface, 18); bubble.setLayout(new BorderLayout()); bubble.setBorder(BorderFactory.createEmptyBorder(12, 16, 12, 16));
            bubble.add(plain(text, false, false));
            JPanel align = new JPanel(new BorderLayout()); align.setOpaque(false); align.setBorder(BorderFactory.createEmptyBorder(0, Math.max(50, (scroll.getViewport().getWidth() - 2 * margin) / 5), 0, 0));
            align.add(bubble); body.add(align);
        } else if (kind.equals("reasoning")) {
            String duration = seconds(num(item.get("durationMs")));
            body.add(disclosure((open ? "▾ " : "▸ ") + ("inProgress".equals(item.get("status")) ? "Thinking…" : "Thought" + (duration.isEmpty() ? "" : " for " + duration)), id));
            if (open) body.add(plain(text, false, false));
        } else if (kind.equals("commandExecution")) {
            boolean running = "inProgress".equals(item.get("status"));
            String result = running ? "◌" : num(item.get("exitCode")) != 0 || !str(item.get("error")).isEmpty() ? "✕" : "✓";
            JPanel head = new JPanel(new BorderLayout(10, 0)); head.setOpaque(false); head.setAlignmentX(Component.LEFT_ALIGNMENT);
            JLabel icon = new JLabel(new Ui.VectorIcon("terminal", 16)); icon.setForeground(Ui.muted); head.add(icon, BorderLayout.WEST);
            String command = str(item.get("command")).replace('\n', ' '); if (command.length() > 64) command = command.substring(0, 64) + "…";
            String description = str(item.get("description")); if (description.isEmpty()) description = yes(item.get("shell")) ? "Shell" : "Run command";
            JLabel name = new JLabel("<html><span style='color:" + color(Ui.muted) + "'>" + (open ? "▾ " : "▸ ") + Markdown.escape(description) + "</span> &nbsp; <code>" + Markdown.escape(command) + "</code></html>");
            head.add(name); JLabel state = Ui.muted(seconds(num(item.get("durationMs"))) + "  " + (running ? "" : result)); if (running) state.setIcon(new Ui.Spinner(14)); head.add(state, BorderLayout.EAST);
            head.setCursor(Cursor.getPredefinedCursor(Cursor.HAND_CURSOR)); head.setFocusable(true); Desktop.bind(head, "ENTER", "toggle", () -> toggle(id)); Desktop.bind(head, "SPACE", "toggle-space", () -> toggle(id));
            MouseAdapter toggle = new MouseAdapter() { public void mouseClicked(MouseEvent e) { if (SwingUtilities.isLeftMouseButton(e)) { head.requestFocusInWindow(); toggle(id); } } };
            head.addMouseListener(toggle); name.addMouseListener(toggle); body.add(head);
            if (open) {
                JPanel output = Ui.rounded(Ui.surface, 12); output.setLayout(new BorderLayout(0, 8)); output.setBorder(BorderFactory.createEmptyBorder(12, 12, 12, 12));
                output.add(plain(clip(commandOutput(item), 65536), true, false));
                JPanel details = new JPanel(new FlowLayout(FlowLayout.LEFT, 8, 0)); details.setOpaque(false);
                String error = str(item.get("error")); if (!error.isEmpty()) details.add(Ui.muted(error));
                if (num(item.get("exitCode")) != 0) details.add(Ui.muted("Exit " + num(item.get("exitCode"))));
                if (num(item.get("job")) > 0) details.add(Ui.muted("Background job #" + num(item.get("job"))));
                if (num(item.get("dropped")) > 0 || yes(item.get("truncated"))) details.add(Ui.muted("Output clipped"));
                details.add(Desktop.button("Show full output", () -> pane.rpc("item/output", map("itemId", id), v -> pane.desktop.text("Command output", str(obj(v).get("output")), null))));
                output.add(details, BorderLayout.SOUTH); body.add(Box.createVerticalStrut(10)); body.add(output);
            }
        } else if (kind.equals("notice") || kind.equals("event") || kind.equals("hook") || kind.equals("compaction")) {
            boolean loaded = title.toLowerCase(Locale.ROOT).contains("loaded") || "loaded".equals(item.get("level"));
            boolean details = loaded || text.contains("\n") || text.length() > 180;
            String summary = loaded ? loadedSummary(item) : title.isEmpty() ? text.lines().findFirst().orElse("") : title;
            if (kind.equals("compaction")) summary = "Context compacted · " + num(item.get("tokensBefore")) + " → " + num(item.get("tokensAfter")) + " tokens";
            if (summary.length() > 150) summary = summary.substring(0, 150) + "…";
            JComponent notice = details ? disclosure((open ? "▾ " : "▸ ") + summary, id) : Ui.muted(summary); if ("error".equals(item.get("level"))) notice.setForeground(Ui.color(0xd65a68)); else if ("warning".equals(item.get("level"))) notice.setForeground(Ui.color(0xb28035)); body.add(notice);
            if (open && details) body.add(plain(text, false, false));
        } else {
            if (kind.equals("goalStatus") && text.isEmpty()) text = str(obj(item.get("goalState")).get("objective"));
            if (!title.isEmpty() && !kind.equals("agentMessage")) body.add(Ui.muted(title));
            String shown = clip(text, 65536);
            if (kind.equals("extText") && !str(item.get("lang")).isEmpty()) body.add(plain(shown, true, "diff".equals(item.get("lang"))));
            else for (Markdown.Block block : Markdown.parse(shown)) {
                if (block.kind().equals("code")) {
                    JPanel code = Ui.rounded(Ui.surface, 12); code.setLayout(new BorderLayout(0, 8)); code.setBorder(BorderFactory.createEmptyBorder(10, 14, 12, 14)); code.setAlignmentX(Component.LEFT_ALIGNMENT);
                    JPanel top = new JPanel(new BorderLayout()); top.setOpaque(false); top.add(Ui.muted(block.language()), BorderLayout.WEST);
                    JButton copy = Ui.icon("copy", "Copy code", () -> Desktop.copy(block.text())); copy.setVisible(false); top.add(copy, BorderLayout.EAST);
                    JPopupMenu codeMenu = new JPopupMenu(); JMenuItem copyCode = new JMenuItem("Copy code"); copyCode.addActionListener(e -> Desktop.copy(block.text())); codeMenu.add(copyCode); code.setComponentPopupMenu(codeMenu);
                    code.add(top, BorderLayout.NORTH); code.add(plain(block.text().stripTrailing(), true, block.language().equals("diff"))); hoverCopy(code, code, copy); body.add(code); body.add(Box.createVerticalStrut(8));
                } else {
                    String html = Markdown.inline(block.text());
                    if (block.kind().equals("heading")) html = "<h" + block.level() + ">" + html + "</h" + block.level() + ">";
                    if (block.kind().equals("list")) html = html.replaceFirst("^[-*+] ", "• &nbsp;");
                    if (block.kind().equals("quote")) html = "<span style='color:" + color(Ui.muted) + "'>│ &nbsp;" + html + "</span>";
                    body.add(html(html)); if (!block.kind().equals("list")) body.add(Box.createVerticalStrut(7));
                }
            }
            if (text.length() > 65536) { String full = text; body.add(Desktop.button("Show all text", () -> pane.desktop.text("Message", full, null))); }
        }
        for (Object status : Json.list(obj(item.get("display")).get("statuses"))) body.add(Ui.muted(str(obj(status).get("text"))));
        int index = 0;
        for (Object value : Json.list(item.get("images"))) {
            Map<String, Object> image = obj(value); String key = id + ":" + index + ":" + str(image.get("file")); int imageIndex = index++;
            JPanel picture = new JPanel(new BorderLayout()); ImageIcon icon = imageCache.get(key);
            picture.add(new JLabel(icon == null ? str(image.getOrDefault("name", "Image")) + " · " + num(image.get("width")) + "×" + num(image.get("height")) : "", icon, JLabel.LEFT), BorderLayout.CENTER);
            picture.add(Desktop.button("View image", () -> {
                ImageIcon preview = imageCache.get(key);
                if (preview == null) { loadingImages.remove(key); loadImage(key, id, imageIndex); return; }
                JDialog viewer = pane.desktop.dialog("Image", new JScrollPane(new JLabel(preview)));
                viewer.add(Desktop.button("Save original…", () -> pane.rpc("item/image", map("itemId", id, "index", imageIndex), result -> {
                    JFileChooser chooser = new JFileChooser(); chooser.setSelectedFile(new java.io.File(str(image.getOrDefault("file", "image.png"))));
                    if (chooser.showSaveDialog(viewer) == JFileChooser.APPROVE_OPTION) {
                        var file = chooser.getSelectedFile().toPath();
                        CompletableFuture.runAsync(() -> { try { java.nio.file.Files.write(file, Base64.getDecoder().decode(str(obj(result).get("data")))); }
                        catch (Exception e) { Desktop.edt(() -> pane.desktop.error(e.getMessage())); } });
                    }
                })), BorderLayout.SOUTH); viewer.pack(); viewer.setVisible(true);
            }), BorderLayout.SOUTH); body.add(picture);
            if (!imageCache.containsKey(key) && !loadingImages.contains(key)) loadImage(key, id, imageIndex);
        }
        for (Component child : body.getComponents()) if (child instanceof JComponent component) { component.setAlignmentX(Component.LEFT_ALIGNMENT); component.setMaximumSize(new Dimension(Integer.MAX_VALUE, component.getPreferredSize().height)); }
        row.add(body, BorderLayout.CENTER); installMenu(row, menu);

        row.setMaximumSize(new Dimension(Integer.MAX_VALUE, row.getPreferredSize().height));
    }
    static void hoverCopy(JComponent component, JComponent code, JButton copy) {
        component.addMouseListener(new MouseAdapter() { public void mouseEntered(MouseEvent e) { copy.setVisible(true); } public void mouseExited(MouseEvent e) { SwingUtilities.invokeLater(() -> { if (code.getMousePosition(true) == null) copy.setVisible(false); }); } });
        for (Component child : component.getComponents()) if (child instanceof JComponent nested) hoverCopy(nested, code, copy);
    }
    static String commandOutput(Map<String, Object> item) {
        String output = str(item.get("output")); return output.isEmpty() ? str(item.get("resultText")) : output;
    }
    static String seconds(long ms) { return ms <= 0 ? "" : String.format(Locale.ROOT, "%.1fs", ms / 1000.0); }
    String loadedSummary(Map<String, Object> item) {
        Map<String, Object> context = pane.loadedContext;
        return "Loaded · " + Json.list(context.get("skills")).size() + " skills · " + Json.list(context.get("extensions")).size() + " extensions · " + str(pane.info.getOrDefault("modelName", pane.info.get("model")));
    }
    void toggle(String id) { if (!expanded.remove(id)) expanded.add(id); dirty = true; }
    JComponent disclosure(String title, String id) {
        JLabel label = Ui.muted(title); label.setBorder(BorderFactory.createEmptyBorder(2, 0, 2, 0)); label.setCursor(Cursor.getPredefinedCursor(Cursor.HAND_CURSOR));
        label.setFocusable(true); Desktop.bind(label, "ENTER", "toggle", () -> toggle(id)); Desktop.bind(label, "SPACE", "toggle-space", () -> toggle(id));
        label.addMouseListener(new MouseAdapter() { public void mouseClicked(MouseEvent e) { if (SwingUtilities.isLeftMouseButton(e)) toggle(id); } }); return label;
    }
    static void installMenu(JComponent c, JPopupMenu menu) {
        if (c.getComponentPopupMenu() == null) c.setComponentPopupMenu(menu); for (Component child : c.getComponents()) if (child instanceof JComponent jc) installMenu(jc, c.getComponentPopupMenu());
    }
    static String clip(String text, int max) { return text.length() <= max ? text : text.substring(0, max) + "\n… clipped · use full-output viewer"; }
    JComponent plain(String text, boolean mono, boolean diff) {
        if (diff) {
            StringBuilder html = new StringBuilder("<pre>");
            for (String line : text.split("\n", -1)) {
                String color = line.startsWith("+") ? "#2f9d50" : line.startsWith("-") ? "#d55c5c" : "";
                html.append(color.isEmpty() ? "" : "<font color='" + color + "'>").append(Markdown.escape(line)).append(color.isEmpty() ? "" : "</font>").append('\n');
            }
            return html(html.append("</pre>").toString());
        }
        JTextArea area = new JTextArea(text); area.setEditable(false); area.setLineWrap(true); area.setWrapStyleWord(!mono);
        area.setFont(mono ? new Font(Font.MONOSPACED, Font.PLAIN, Math.max(12, pane.desktop.fontSize - 1)) : Ui.body(pane.desktop.fontSize));
        area.setOpaque(false); area.setForeground(Ui.text); area.setAlignmentX(Component.LEFT_ALIGNMENT); return area;
    }
    JEditorPane html(String body) {
        JEditorPane area = new JEditorPane(); area.putClientProperty(JEditorPane.W3C_LENGTH_UNITS, true); area.putClientProperty(JEditorPane.HONOR_DISPLAY_PROPERTIES, true); area.setFont(Ui.body(pane.desktop.fontSize)); area.setContentType("text/html"); area.setText( "<html><head><style>body { margin:0; } h1 {font-size:22px; margin:8px 0;} h2 {font-size:19px; margin:6px 0;} h3 {font-size:16px;} code {font-family:monospace;} a {color:" + color(Ui.accent) + ";}</style></head><body style='font-family:sans-serif;font-size:" + pane.desktop.fontSize + "px;color:" + color(UIManager.getColor("Label.foreground")) + "'>" + body + "</body></html>");
        area.setEditable(false); area.setOpaque(false); area.setForeground(Ui.text); area.setAlignmentX(Component.LEFT_ALIGNMENT);
        int width = Math.max(260, scroll.getViewport().getWidth() - 2 * Math.max(28, (scroll.getViewport().getWidth() - 790) / 2) - 8); area.setSize(width, Short.MAX_VALUE);
        area.setPreferredSize(new Dimension(width, area.getPreferredSize().height));
        area.addHyperlinkListener(e -> {
            if (e.getEventType() == HyperlinkEvent.EventType.ACTIVATED && Markdown.safeLink(e.getDescription()))
                CompletableFuture.runAsync(() -> { try { java.awt.Desktop.getDesktop().browse(URI.create(e.getDescription())); } catch (Exception ex) { Desktop.edt(() -> pane.desktop.error(ex.getMessage())); } });
        }); return area;
    }
    static String color(Color c) { return c == null ? "#222222" : String.format("#%02x%02x%02x", c.getRed(), c.getGreen(), c.getBlue()); }
    void loadImage(String key, String id, int index) {
        if (!loadingImages.add(key)) return;
        pane.desktop.core.call("item/image", pane.id, map("itemId", id, "index", index, "preview", true, "offline", yes(pane.info.get("offline")))).thenApplyAsync(v -> {
            try {
                byte[] bytes = Base64.getDecoder().decode(str(obj(v).get("data")));
                java.awt.image.BufferedImage image;
                try (var stream = ImageIO.createImageInputStream(new ByteArrayInputStream(bytes))) {
                    var readers = ImageIO.getImageReaders(stream);
                    if (!readers.hasNext()) throw new IllegalArgumentException("JDK cannot decode this image format (WebP may need conversion to PNG)");
                    var reader = readers.next();
                    try {
                        reader.setInput(stream); int w = reader.getWidth(0), h = reader.getHeight(0);
                        if (w <= 0 || h <= 0 || (long)w * h > 64_000_000) throw new IllegalArgumentException("Image dimensions exceed preview limit");
                        image = reader.read(0);
                    } finally { reader.dispose(); }
                }
                double scale = Math.min(1, Math.min(600.0 / image.getWidth(), 350.0 / image.getHeight()));
                return new ImageIcon(image.getScaledInstance(Math.max(1, (int)(image.getWidth() * scale)), Math.max(1, (int)(image.getHeight() * scale)), Image.SCALE_SMOOTH));
            } catch (Exception e) { throw new CompletionException(e); }
        }).whenComplete((icon, e) -> Desktop.edt(() -> {
            if (e == null) { imageCache.put(key, icon); fingerprints.remove(id); dirty = true; }
            else { imageCache.put(key, new ImageIcon()); pane.desktop.connection.setText("Image preview: " + e.getMessage()); }
            // Failed loads are not automatically retried on every streaming delta.
        }));
    }
}
