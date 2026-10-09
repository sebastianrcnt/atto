package atto.swing;

import javax.imageio.ImageIO;
import javax.swing.*;
import java.awt.*;
import java.awt.image.BufferedImage;
import java.awt.datatransfer.*;
import java.nio.file.*;
import java.util.*;
import java.util.List;
import java.util.concurrent.*;
import java.util.function.*;
import static atto.swing.Json.*;

/** Opt-in GUI automation. Requests/waits run on a worker; component actions and painting use the EDT. */
final class ScreenshotScript {
    private ScreenshotScript() {}
    static void start(Desktop desktop) {
        Thread worker = new Thread(() -> {
            try { run(desktop); System.out.println("Swing screenshot script passed"); }
            catch (Throwable e) { e.printStackTrace(); System.exit(1); }
            finally { Desktop.edt(desktop::quit); }
        }, "swing-screenshot-script"); worker.start();
    }
    static <T> T edt(Supplier<T> action) throws Exception {
        FutureTask<T> task = new FutureTask<>(action::get); SwingUtilities.invokeAndWait(task); return task.get();
    }
    static SessionPane pane(Desktop d) { return (SessionPane)d.tabs.getSelectedComponent(); }
    static void await(Callable<Boolean> condition) throws Exception {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(30);
        while (!condition.call()) { if (System.nanoTime() > deadline) throw new AssertionError("GUI condition timed out"); Thread.sleep(50); }
    }
    static void run(Desktop d) throws Exception {
        Path script = Path.of(d.options.screenshotScript()).toAbsolutePath();
        await(() -> edt(() -> d.core.protocol.ready && pane(d) != null));
        for (Object value : list(parse(Files.readString(script)))) {
            Map<String, Object> step = obj(value); String action = str(step.get("action"));
            switch (action) {
                case "sleep" -> Thread.sleep(num(step.get("ms")));
                case "idle" -> await(() -> edt(() -> !yes(pane(d).info.get("busy")) && list(pane(d).info.get("items")).stream().noneMatch(x -> "inProgress".equals(obj(x).get("status")))));
                case "connected" -> { await(() -> edt(() -> d.core.protocol.ready)); Thread.sleep(500); }
                case "prompt" -> await(() -> edt(() -> { Map<String, Object> prompt = obj(pane(d).info.get("prompt")); return !prompt.isEmpty() && (!step.containsKey("title") || str(prompt.get("title")).contains(str(step.get("title")))); }));
                case "promptText" -> edt(() -> { JDialog prompt = d.prompts.get(pane(d).id); if (prompt == null || !setInput(prompt, str(step.get("text")))) throw new AssertionError("Prompt input missing"); return null; });
                case "type" -> edt(() -> { pane(d).composer.setText(str(step.get("text"))); pane(d).composer.setCaretPosition(pane(d).composer.getText().length()); pane(d).composer.requestFocusInWindow(); return null; });
                case "send" -> edt(() -> { pane(d).completionPopup.setVisible(false); pane(d).submit(str(step.getOrDefault("intent", "auto"))); return null; });
                case "complete" -> edt(() -> { pane(d).complete(); return null; });
                case "chooseCompletion" -> edt(() -> { pane(d).completionList.setSelectedIndex((int)num(step.get("index"))); pane(d).chooseCompletion(); return null; });
                case "action" -> { edt(() -> { Action command = d.actions.get(str(step.get("name"))); if (command == null) throw new IllegalArgumentException("Unknown UI action " + step); SwingUtilities.invokeLater(() -> command.actionPerformed(null)); return null; }); Thread.sleep(150); }
                case "chooseFile" -> {
                    Path target = script.getParent().resolve(str(step.get("path"))).normalize(); Files.createDirectories(target.getParent()); if (yes(step.get("directory"))) Files.createDirectories(target);
                    await(() -> edt(() -> findChooser() != null)); edt(() -> { JFileChooser chooser = findChooser(); chooser.setSelectedFile(target.toFile()); chooser.approveSelection(); return null; });
                }
                case "assertFile" -> { Path file = script.getParent().resolve(str(step.get("path"))).normalize(); await(() -> Files.exists(file) && Files.size(file) > 0); }
                case "theme" -> edt(() -> { d.applyTheme(str(step.get("value"))); d.refreshTheme(); return null; });
                case "click" -> edt(() -> { String label = str(step.get("text")); for (Window w : Window.getWindows()) if (w.isShowing() && click(w, label)) return null; throw new IllegalArgumentException("Button not found: " + label); });
                case "dismiss" -> edt(() -> { for (Window w : d.window.getOwnedWindows()) if (w instanceof JDialog) w.dispose(); pane(d).completionPopup.setVisible(false); return null; });
                case "rpc" -> {
                    String id = edt(() -> pane(d).id); variables.put("original", id); Object reply = d.core.call(str(step.get("method")), id, obj(step.get("params"))).get(30, TimeUnit.SECONDS);
                    if (step.containsKey("saveId")) variables.put(str(step.get("saveId")), str(obj(reply).get("threadId")));
                }
                case "answer" -> {
                    Map<String, Object> params = new LinkedHashMap<>(obj(step.get("params"))); String id = edt(() -> pane(d).id); params.put("id", edt(() -> obj(pane(d).info.get("prompt")).get("id")));
                    d.core.call("prompt/answer", id, params).get(30, TimeUnit.SECONDS);
                }
                case "open" -> {
                    Map<String, Object> params = new LinkedHashMap<>(obj(step.get("params"))); if (step.containsKey("id")) params.put("threadId", variables.get(str(step.get("id"))));
                    d.core.hydrate(str(step.get("method")), params).get(30, TimeUnit.SECONDS); Thread.sleep(300);
                }
                case "tab" -> edt(() -> { d.tabs.setSelectedIndex((int)num(step.get("index"))); return null; });
                case "expand" -> edt(() -> { SessionPane p = pane(d); for (Object item : list(p.info.get("items"))) if (str(step.get("type")).equals(obj(item).get("type"))) p.transcript.expanded.add(str(obj(item).get("id"))); p.transcript.invalidateRows(); return null; });
                case "contextMenu", "fork" -> edt(() -> {
                    SessionPane p = pane(d); Map<String, Object> message = list(p.info.get("items")).stream().map(Json::obj).filter(i -> "userMessage".equals(i.get("type")) && !str(i.get("entryId")).isEmpty()).findFirst().orElseThrow();
                    JPanel row = p.transcript.rendered.get(str(message.get("id"))); if (row == null) throw new AssertionError("User message not rendered"); JPopupMenu menu = row.getComponentPopupMenu();
                    if (action.equals("contextMenu")) { p.transcript.following = false; row.scrollRectToVisible(new Rectangle(0, 0, row.getWidth(), row.getHeight())); menu.show(row, 150, 24); }
                    else { menu.setVisible(false); for (Component entry : menu.getComponents()) if (entry instanceof JMenuItem command && command.getText().equals("Fork from this message")) command.doClick(); }
                    return null;
                });
                case "pasteImage" -> edt(() -> { Toolkit.getDefaultToolkit().getSystemClipboard().setContents(new ImageTransfer(sampleImage()), null); pane(d).pasteImage(); return null; });
                case "dropImage" -> {
                    Path file = Files.createTempFile("atto-drop-", ".png"); try {
                        ImageIO.write(sampleImage(), "png", file.toFile());
                        edt(() -> { if (!pane(d).composer.getTransferHandler().importData(new TransferHandler.TransferSupport(pane(d).composer, new FileTransfer(file.toFile())))) throw new AssertionError("File drop rejected"); return null; });
                        await(() -> edt(() -> !pane(d).images.isEmpty()));
                    } finally { Files.deleteIfExists(file); }
                }
                case "clearImages" -> edt(() -> { pane(d).images.clear(); pane(d).updateAttachments(); return null; });
                case "killServer" -> {
                    ProcessHandle process = ProcessHandle.current().children().filter(p -> p.info().arguments().map(a -> Arrays.asList(a).contains("app-server")).orElse(false)).findFirst().orElseThrow();
                    process.destroyForcibly(); await(() -> !d.core.protocol.ready);
                }
                case "scroll" -> { Thread.sleep(300); edt(() -> { SessionPane p = pane(d); p.transcript.following = false; p.transcript.userScrollVersion++;
                    int y = 0; if (str(step.get("to")).equals("image")) { for (Object item : list(p.info.get("items"))) if (!list(obj(item).get("images")).isEmpty()) { JPanel row = p.transcript.rendered.get(str(obj(item).get("id"))); if (row != null) y = row.getY(); } }
                    p.transcript.scroll.getVerticalScrollBar().setValue(Math.max(0, y - 12)); return null;
                }); }
                case "detach" -> edt(() -> { SessionPane p = pane(d); variables.put("detached", p.id); d.detach(p, false); return null; });
                case "assertState" -> edt(() -> {
                    if (step.containsKey("writable") && pane(d).writable() != yes(step.get("writable"))) throw new AssertionError("Writable state mismatch");
                    if (step.containsKey("selectedTab") && d.tabs.getSelectedIndex() != num(step.get("selectedTab"))) throw new AssertionError("Selected tab changed");
                    if (step.containsKey("tabs") && d.tabs.getTabCount() != num(step.get("tabs"))) throw new AssertionError("Tab count mismatch");
                    if (step.containsKey("images") && pane(d).images.size() != num(step.get("images"))) throw new AssertionError("Attachment count mismatch");
                    return null;
                });
                case "assertClipboard" -> { Object text = Toolkit.getDefaultToolkit().getSystemClipboard().getData(DataFlavor.stringFlavor); if (!str(text).contains(str(step.get("text")))) throw new AssertionError("Clipboard text missing"); }
                case "assert" -> edt(() -> { String expected = str(step.get("text")); if (!componentText(d.window).contains(expected) && !write(pane(d).info).contains(expected)) throw new AssertionError("Missing GUI text: " + expected); return null; });
                case "screenshot" -> {
                    edt(() -> { d.refreshSessions(); return null; }); Thread.sleep(500); Path file = script.getParent().resolve(str(step.get("file"))); Files.createDirectories(file.getParent());
                    BufferedImage image = edt(() -> paint(d)); ImageIO.write(image, "png", file.toFile()); System.out.println("Screenshot: " + file);
                }
                default -> throw new IllegalArgumentException("Unknown screenshot action " + action);
            }
        }
    }
    static final Map<String, String> variables = new HashMap<>();
    static JFileChooser findChooser() { for (Window window : Window.getWindows()) if (window.isShowing()) { JFileChooser chooser = findChooser(window); if (chooser != null) return chooser; } return null; }
    static JFileChooser findChooser(Container parent) { for (Component child : parent.getComponents()) { if (child instanceof JFileChooser chooser) return chooser; if (child instanceof Container nested) { JFileChooser chooser = findChooser(nested); if (chooser != null) return chooser; } } return null; }
    static boolean setInput(Container c, String text) { for (Component child : c.getComponents()) { if (child instanceof JTextField field) { field.setText(text); return true; } if (child instanceof Container nested && setInput(nested, text)) return true; } return false; }
    static boolean click(Container c, String text) {
        for (Component child : c.getComponents()) { if (child instanceof AbstractButton b && b.getText().equals(text) && b.isEnabled()) { b.doClick(); return true; } if (child instanceof Container nested && click(nested, text)) return true; } return false;
    }
    static String componentText(Container c) {
        StringBuilder text = new StringBuilder(); for (Component child : c.getComponents()) {
            if (child instanceof JLabel l) text.append(l.getText()); if (child instanceof javax.swing.text.JTextComponent t) text.append(t.getText()); if (child instanceof Container nested) text.append(componentText(nested));
        } return text.toString();
    }
    static BufferedImage paint(Desktop d) {
        int scale = 2; JRootPane root = d.window.getRootPane(); BufferedImage image = new BufferedImage(root.getWidth() * scale, root.getHeight() * scale, BufferedImage.TYPE_INT_RGB);
        Graphics2D g = image.createGraphics(); g.scale(scale, scale); Ui.smooth(g); root.paint(g);
        for (Window w : d.window.getOwnedWindows()) if (w.isShowing()) {
            Point location = w.getLocation(); Point origin = d.window.getRootPane().getLocationOnScreen(); Graphics2D overlay = (Graphics2D)g.create(); overlay.translate(Math.max(0, location.x - origin.x), Math.max(0, location.y - origin.y)); if (w instanceof JDialog dialog) { overlay.setColor(new Color(0, 0, 0, 40)); overlay.fillRoundRect(-5, -5, w.getWidth() + 10, w.getHeight() + 10, 18, 18); dialog.getRootPane().paint(overlay); } else w.paint(overlay); overlay.dispose();
        }
        if (pane(d) != null && pane(d).completionPopup.isVisible()) {
            JPopupMenu popup = pane(d).completionPopup; Point location = popup.getLocationOnScreen(), origin = d.window.getRootPane().getLocationOnScreen(); Graphics2D overlay = (Graphics2D)g.create(); overlay.translate(location.x - origin.x, location.y - origin.y); popup.paint(overlay); overlay.dispose();
        }
        MenuElement[] menu = MenuSelectionManager.defaultManager().getSelectedPath(); for (MenuElement entry : menu) if (entry instanceof JPopupMenu popup && popup.isVisible() && popup != pane(d).completionPopup) {
            Point location = popup.getLocationOnScreen(), origin = d.window.getRootPane().getLocationOnScreen(); Graphics2D overlay = (Graphics2D)g.create(); overlay.translate(location.x - origin.x, location.y - origin.y); popup.paint(overlay); overlay.dispose();
        }
        g.dispose(); return image;
    }
    record ImageTransfer(Image image) implements Transferable {
        public DataFlavor[] getTransferDataFlavors() { return new DataFlavor[]{DataFlavor.imageFlavor}; }
        public boolean isDataFlavorSupported(DataFlavor flavor) { return DataFlavor.imageFlavor.equals(flavor); }
        public Object getTransferData(DataFlavor flavor) throws UnsupportedFlavorException { if (!isDataFlavorSupported(flavor)) throw new UnsupportedFlavorException(flavor); return image; }
    }
    record FileTransfer(java.io.File file) implements Transferable {
        public DataFlavor[] getTransferDataFlavors() { return new DataFlavor[]{DataFlavor.javaFileListFlavor}; }
        public boolean isDataFlavorSupported(DataFlavor flavor) { return DataFlavor.javaFileListFlavor.equals(flavor); }
        public Object getTransferData(DataFlavor flavor) throws UnsupportedFlavorException { if (!isDataFlavorSupported(flavor)) throw new UnsupportedFlavorException(flavor); return List.of(file); }
    }
    static BufferedImage sampleImage() {
        BufferedImage image = new BufferedImage(440, 220, BufferedImage.TYPE_INT_RGB); Graphics2D g = image.createGraphics(); Ui.smooth(g);
        g.setColor(Ui.color(0xe9edff)); g.fillRect(0, 0, 440, 220); g.setColor(Ui.color(0x526bd8)); g.fillRoundRect(32, 36, 160, 148, 22, 22);
        g.setColor(Ui.color(0x202631)); g.setFont(Ui.body(22)); g.drawString("Workspace sketch", 215, 96); g.setFont(Ui.body(14)); g.drawString("Paste or drop images", 215, 126); g.dispose(); return image;
    }
}
