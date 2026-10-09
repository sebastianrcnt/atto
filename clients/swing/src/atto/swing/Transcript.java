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
        scroll.getVerticalScrollBar().setUnitIncrement(22);
        scroll.addMouseWheelListener(e -> { if (e.getWheelRotation() < 0) { following = false; userScrollVersion++; bottom.setVisible(true); } });
        scroll.getVerticalScrollBar().addMouseListener(new MouseAdapter() { public void mousePressed(MouseEvent e) { following = false; userScrollVersion++; bottom.setVisible(true); } });
        scroll.getVerticalScrollBar().addAdjustmentListener(e -> {
            if (adjusting) return;
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
    void loadEarlier() { page += 200; fingerprints.clear(); dirty = true; }
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
        row.setBorder(BorderFactory.createCompoundBorder(BorderFactory.createMatteBorder(0, 0, 1, 0, UIManager.getColor("Separator.foreground")), BorderFactory.createEmptyBorder(10, 12, 12, 12)));
        row.setAlignmentX(Component.LEFT_ALIGNMENT);
        JPanel header = new JPanel(new FlowLayout(FlowLayout.LEFT, 6, 0));
        String title = switch (kind) {
            case "userMessage" -> "You"; case "agentMessage" -> "atto"; case "reasoning" -> "Reasoning";
            case "commandExecution" -> (yes(item.get("shell")) ? "! " : "Tool · ") + str(item.get("description"));
            case "branchSummary" -> "Branch summary"; case "compaction" -> "Compaction · " + num(item.get("tokensBefore")) + " → " + num(item.get("tokensAfter"));
            case "goalStatus" -> "Goal · " + str(item.get("goalStatus"));
            default -> str(item.getOrDefault("title", kind));
        };
        JLabel label = new JLabel(title + " · " + str(item.get("status")) + (num(item.get("durationMs")) > 0 ? " · " + num(item.get("durationMs")) / 1000.0 + "s" : ""));
        label.setFont(label.getFont().deriveFont(Font.BOLD)); header.add(label);
        if (kind.equals("reasoning") || kind.equals("commandExecution") || kind.equals("extText")) header.add(Desktop.button(open ? "Collapse" : "Expand", () -> { if (!expanded.remove(id)) expanded.add(id); dirty = true; }));
        header.add(Desktop.button("Copy", () -> Desktop.copy(kind.equals("commandExecution") ? str(item.get("command")) + "\n" + str(item.get("output")) : str(item.get("text")))));
        row.add(header, BorderLayout.NORTH); JPanel body = new JPanel(); body.setLayout(new BoxLayout(body, BoxLayout.Y_AXIS));
        String entry = str(item.get("entryId"));
        JPopupMenu menu = new JPopupMenu();
        menu.add(new JMenuItem(new AbstractAction("Fork from this message") { public void actionPerformed(ActionEvent e) { if (!entry.isEmpty()) Panels.fork(pane, entry); } }));
        menu.add(new JMenuItem(new AbstractAction("Label this entry") { public void actionPerformed(ActionEvent e) { if (!entry.isEmpty()) pane.desktop.input("Entry label", "", text -> pane.rpc("thread/setLabel", map("entryId", entry, "label", text), v -> {})); } }));
        row.setComponentPopupMenu(menu); header.setComponentPopupMenu(menu);
        if (kind.equals("commandExecution")) {
            body.add(plain(str(item.get("command")), true, false));
            String output = str(item.get("output"));
            if (!open) {
                String[] lines = output.split("\n", -1); if (lines.length > 12) output = String.join("\n", Arrays.copyOfRange(lines, lines.length - 12, lines.length));
            }
            body.add(plain(clip(output, open ? 65536 : 5000), true, true));
            List<String> details = new ArrayList<>();
            for (String key : List.of("exitCode", "job", "background", "dropped", "error", "resultText", "reason", "cap")) if (item.containsKey(key)) details.add(key + ": " + str(item.get(key)));
            for (String key : List.of("timedOut", "canceled", "excluded", "truncated", "pending")) if (yes(item.get(key))) details.add(key);
            body.add(plain(String.join(" · ", details), false, false));
            body.add(Desktop.button("Show full available output", () -> pane.rpc("item/output", map("itemId", id), v -> pane.desktop.text("Command output" + (yes(obj(v).get("truncated")) ? " (clipped by server)" : ""), str(obj(v).get("output")), null))));
        } else if (!kind.equals("reasoning") || open) {
            Map<String, Object> display = obj(item.get("display"));
            String text = display.containsKey("text") ? str(display.get("text")) : str(item.get("text"));
            if (text.isEmpty() && kind.equals("goalStatus")) text = Panels.pretty(item.get("goalState"));
            if (text.isEmpty() && !kind.equals("agentMessage")) text = Panels.pretty(item);
            String shown = clip(text, 65536);
            if (List.of("notice", "event", "goal", "hook", "goalStatus").contains(kind)) {
                body.add(plain(shown, "loaded".equals(item.get("level")), false));
            } else if (kind.equals("extText") && !str(item.get("lang")).isEmpty()) {
                String[] lines = shown.split("\n", -1); int preview = (int)num(item.get("preview"));
                if (!open && preview > 0 && lines.length > preview) shown = String.join("\n", Arrays.copyOf(lines, preview)) + "\n… expand for more";
                body.add(plain(shown, true, "diff".equals(item.get("lang"))));
            } else for (Markdown.Block block : Markdown.parse(shown)) {
                if (block.kind().equals("code")) {
                    JPanel code = new JPanel(new BorderLayout()); code.add(plain(block.text(), true, block.language().equals("diff")), BorderLayout.CENTER);
                    code.add(Desktop.button("Copy " + block.language(), () -> Desktop.copy(block.text())), BorderLayout.NORTH); body.add(code);
                } else {
                    String html = Markdown.inline(block.text());
                    if (block.kind().equals("heading")) html = "<h" + block.level() + ">" + html + "</h" + block.level() + ">";
                    body.add(html(html));
                }
            }
            String fullText = text;
            if (text.length() > 65536) body.add(Desktop.button("Show all text", () -> pane.desktop.text(title, fullText, null)));
            for (Object s : Json.list(display.get("statuses"))) body.add(plain(str(obj(s).get("ext")) + ": " + str(obj(s).get("text")), false, false));
        }
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
        row.add(body, BorderLayout.CENTER);
        row.setMaximumSize(new Dimension(Integer.MAX_VALUE, row.getPreferredSize().height));
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
        area.setFont(new Font(mono ? Font.MONOSPACED : Font.SANS_SERIF, Font.PLAIN, pane.desktop.fontSize));
        area.setBackground(UIManager.getColor("Panel.background")); area.setAlignmentX(Component.LEFT_ALIGNMENT); return area;
    }
    JEditorPane html(String body) {
        JEditorPane area = new JEditorPane("text/html", "<html><body style='font-family:sans-serif;font-size:" + pane.desktop.fontSize + "pt;color:" + color(UIManager.getColor("Label.foreground")) + "'>" + body + "</body></html>");
        area.setEditable(false); area.setBackground(UIManager.getColor("Panel.background")); area.setAlignmentX(Component.LEFT_ALIGNMENT);
        int width = Math.max(300, scroll.getViewport().getWidth() - 30); area.setSize(width, Short.MAX_VALUE);
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
