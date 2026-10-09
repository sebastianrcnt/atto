package atto.swing;

import javax.swing.*;
import javax.swing.border.AbstractBorder;
import javax.swing.plaf.basic.BasicButtonUI;
import javax.swing.plaf.basic.BasicTabbedPaneUI;
import javax.swing.plaf.basic.BasicGraphicsUtils;
import java.awt.*;
import java.awt.geom.*;
import java.time.*;
import java.util.List;

/** The desktop's small shared visual vocabulary; every icon is resolution independent. */
final class Ui {
    private Ui() {}
    static boolean dark;
    static Color canvas, surface, raised, text, muted, line, accent, selected;
    static Font body(int size) { return new Font(System.getProperty("os.name").startsWith("Mac") ? "Helvetica Neue" : Font.SANS_SERIF, Font.PLAIN, size); }
    static void theme(boolean isDark) {
        dark = isDark;
        canvas = color(isDark ? 0x202124 : 0xffffff); surface = color(isDark ? 0x26282c : 0xf5f6f8);
        raised = color(isDark ? 0x303238 : 0xeef0f4); text = color(isDark ? 0xe9ebef : 0x202631);
        muted = color(isDark ? 0xa2a9b5 : 0x727b89); line = color(isDark ? 0x3c4047 : 0xe3e6ec);
        accent = color(isDark ? 0x9baeff : 0x526bd8); selected = color(isDark ? 0x343d59 : 0xe8edff);
        for (String key : List.of("Panel", "Viewport", "TextArea", "TextPane", "EditorPane", "List", "TextField", "PasswordField", "TabbedPane", "ScrollPane", "Label", "Button", "ToggleButton", "ComboBox", "Tree", "Table", "ToolBar", "MenuBar", "Menu", "MenuItem", "PopupMenu", "OptionPane", "CheckBox", "RadioButton")) {
            UIManager.put(key + ".background", canvas); UIManager.put(key + ".foreground", text); UIManager.put(key + ".font", body(14));
        }
        for (String key : List.of("TextArea", "TextField", "EditorPane", "List", "Tree", "Table")) {
            UIManager.put(key + ".selectionBackground", selected); UIManager.put(key + ".selectionForeground", text); UIManager.put(key + ".caretForeground", accent);
        }
        UIManager.put("Separator.foreground", line); UIManager.put("TabbedPane.selected", canvas);
        UIManager.put("TabbedPane.contentAreaColor", canvas); UIManager.put("TabbedPane.focus", accent);
        UIManager.put("ScrollBar.thumb", raised); UIManager.put("ScrollBar.track", canvas);
        UIManager.put("ProgressBar.foreground", accent); UIManager.put("ProgressBar.background", raised);
        UIManager.put("swing.aatext", true);
    }
    static Color color(int rgb) { return new Color(rgb); }
    static JPanel rounded(Color background, int radius) {
        JPanel panel = new JPanel() { protected void paintComponent(Graphics g) {
            Graphics2D p = (Graphics2D)g.create(); smooth(p); p.setColor(getBackground()); p.fillRoundRect(0, 0, getWidth(), getHeight(), radius, radius); p.dispose();
        } };
        panel.putClientProperty("atto.role", background.equals(surface) ? "surface" : background.equals(selected) ? "selected" : "raised"); panel.setOpaque(false); panel.setBackground(background); return panel;
    }
    static void smooth(Graphics2D g) { g.setRenderingHint(RenderingHints.KEY_ANTIALIASING, RenderingHints.VALUE_ANTIALIAS_ON); g.setRenderingHint(RenderingHints.KEY_TEXT_ANTIALIASING, RenderingHints.VALUE_TEXT_ANTIALIAS_ON); }
    static JButton button(String text, Runnable action) {
        JButton b = new JButton(text) { protected void paintComponent(Graphics g) {
            Graphics2D p = (Graphics2D)g.create(); smooth(p); boolean primary = Boolean.TRUE.equals(getClientProperty("primary")) && isEnabled();
            p.setColor(!isEnabled() ? raised : primary ? accent : getModel().isRollover() || getModel().isPressed() ? raised : canvas);
            p.fillRoundRect(0, 0, getWidth() - 1, getHeight() - 1, 12, 12); p.dispose();
            setForeground(!isEnabled() ? muted : primary ? (dark ? color(0x19213c) : Color.WHITE) : Ui.text); super.paintComponent(g);
        } }; b.setUI(new FlatButtonUI()); b.setFont(body(13)); b.setForeground(Ui.text);
        b.setOpaque(false); b.setContentAreaFilled(false); b.setFocusPainted(false); b.setBorder(new RoundBorder());
        b.setMargin(new Insets(6, 10, 6, 10)); b.setCursor(Cursor.getPredefinedCursor(Cursor.HAND_CURSOR)); b.addActionListener(e -> action.run());
        b.putClientProperty("atto.button", true); return b;
    }
    static final class FlatButtonUI extends BasicButtonUI {
        protected void paintText(Graphics g, JComponent c, Rectangle bounds, String text) {
            if (c.isEnabled()) { super.paintText(g, c, bounds, text); return; }
            g.setColor(muted); BasicGraphicsUtils.drawStringUnderlineCharAt(g, text, -1, bounds.x, bounds.y + g.getFontMetrics().getAscent());
        }
    }
    static final class RoundBorder extends AbstractBorder {
        public Insets getBorderInsets(Component c) { return new Insets(7, 11, 7, 11); }
        public void paintBorder(Component c, Graphics g, int x, int y, int w, int h) {
            JButton b = (JButton)c; Graphics2D p = (Graphics2D)g.create(); smooth(p);
            boolean primary = Boolean.TRUE.equals(b.getClientProperty("primary")) && b.isEnabled();
            p.setColor(primary ? accent : b.getModel().isRollover() || b.getModel().isPressed() ? raised : canvas);
            p.setColor(primary ? accent : line); p.drawRoundRect(x, y, w - 1, h - 1, 12, 12); p.dispose();
            b.setForeground(!b.isEnabled() ? muted : primary ? (dark ? color(0x19213c) : Color.WHITE) : text);
        }
    }
    static JButton icon(String name, String tooltip, Runnable action) {
        JButton b = button("", action); b.putClientProperty("atto.icon", true); b.setIcon(new VectorIcon(name, 18)); b.setToolTipText(tooltip); b.setBorder(BorderFactory.createEmptyBorder(7, 8, 7, 8)); return b;
    }
    static final class VectorIcon implements Icon {
        final String name; final int size;
        VectorIcon(String name, int size) { this.name = name; this.size = size; }
        public int getIconWidth() { return size; } public int getIconHeight() { return size; }
        public void paintIcon(Component c, Graphics g, int x, int y) {
            Graphics2D p = (Graphics2D)g.create(); smooth(p); p.translate(x, y); p.scale(size / 20.0, size / 20.0);
            p.setColor(c.isEnabled() ? c.getForeground() : muted); p.setStroke(new BasicStroke(1.6f, BasicStroke.CAP_ROUND, BasicStroke.JOIN_ROUND));
            switch (name) {
                case "plus" -> { p.drawLine(10, 4, 10, 16); p.drawLine(4, 10, 16, 10); }
                case "close" -> { p.drawLine(6, 6, 14, 14); p.drawLine(14, 6, 6, 14); }
                case "send" -> { p.drawLine(10, 16, 10, 4); p.drawLine(4, 10, 10, 4); p.drawLine(16, 10, 10, 4); }
                case "stop" -> p.fillRoundRect(5, 5, 10, 10, 3, 3);
                case "more" -> { for (int a : new int[]{4, 10, 16}) p.fillOval(a - 1, 9, 3, 3); }
                case "search" -> { p.drawOval(3, 3, 10, 10); p.drawLine(12, 12, 17, 17); }
                case "tree" -> { p.drawLine(5, 3, 5, 15); p.drawLine(5, 8, 13, 8); p.drawLine(5, 15, 13, 15); p.drawRoundRect(12, 5, 5, 5, 1, 1); p.drawRoundRect(12, 12, 5, 5, 1, 1); }
                case "jobs" -> { p.drawRoundRect(3, 5, 14, 12, 2, 2); p.drawRect(7, 2, 6, 3); p.drawLine(3, 10, 17, 10); }
                case "copy" -> { p.drawRoundRect(7, 6, 10, 11, 2, 2); p.drawLine(12, 3, 3, 3); p.drawLine(3, 3, 3, 13); }
                case "attach" -> { p.rotate(-.5, 10, 10); p.draw(new Arc2D.Double(5, 1, 10, 18, 0, 180, Arc2D.OPEN)); p.drawLine(5, 10, 5, 14); p.draw(new Arc2D.Double(5, 10, 7, 8, 180, 180, Arc2D.OPEN)); p.drawLine(12, 14, 12, 6); p.drawLine(8, 7, 8, 13); }
                case "terminal" -> { p.drawRoundRect(2, 3, 16, 14, 3, 3); p.drawLine(5, 7, 8, 10); p.drawLine(8, 10, 5, 13); p.drawLine(11, 13, 15, 13); }
                default -> { p.drawRoundRect(3, 3, 14, 14, 3, 3); p.drawLine(7, 7, 13, 7); p.drawLine(7, 11, 13, 11); }
            }
            p.dispose();
        }
    }
    static final class Spinner implements Icon {
        final int size; Spinner(int size) { this.size = size; }
        public int getIconWidth() { return size; } public int getIconHeight() { return size; }
        public void paintIcon(Component c, Graphics g, int x, int y) {
            Graphics2D p = (Graphics2D)g.create(); smooth(p); p.setColor(accent); p.setStroke(new BasicStroke(1.6f, BasicStroke.CAP_ROUND, BasicStroke.JOIN_ROUND));
            int angle = (int)(System.nanoTime() / 3_000_000 % 360); p.drawArc(x + 2, y + 2, size - 4, size - 4, -angle, 245); p.dispose();
        }
    }
    static final class Field extends JTextField {
        final String placeholder;
        Field(String placeholder) { this.placeholder = placeholder; setBorder(BorderFactory.createCompoundBorder(BorderFactory.createLineBorder(line), BorderFactory.createEmptyBorder(8, 10, 8, 10))); }
        protected void paintComponent(Graphics g) { super.paintComponent(g); if (getText().isEmpty()) { g.setColor(muted); g.setFont(getFont()); g.drawString(placeholder, 11, (getHeight() + getFontMetrics(getFont()).getAscent()) / 2 - 2); } }
    }
    static final class Composer extends JTextArea {
        String placeholder = "Ask atto… / for commands, @ for files, ! for shell";
        Composer() { super(3, 40); }
        protected void paintComponent(Graphics g) {
            super.paintComponent(g); if (getText().isEmpty()) { g.setColor(muted); g.setFont(getFont()); g.drawString(placeholder, 4, getFontMetrics(getFont()).getAscent() + 4); }
        }
    }
    static void restyle(Component component) {
        if (component instanceof JComponent c) {
            Object role = c.getClientProperty("atto.role");
            c.setBackground("surface".equals(role) ? surface : "selected".equals(role) ? selected : "raised".equals(role) ? raised : canvas);
            c.setForeground("muted".equals(role) ? muted : text);
            if (c instanceof JButton b && Boolean.TRUE.equals(b.getClientProperty("atto.button"))) {
                b.setUI(new FlatButtonUI()); b.setOpaque(false); b.setContentAreaFilled(false);
                b.setBorder(Boolean.TRUE.equals(b.getClientProperty("atto.icon")) ? BorderFactory.createEmptyBorder(7, 8, 7, 8) : new RoundBorder());
            }
            if (c instanceof JList<?> list) { list.setSelectionBackground(selected); list.setSelectionForeground(text); }
            if (c instanceof JTabbedPane tab) tab.setUI(new Tabs());
        }
        if (component instanceof Container parent) for (Component child : parent.getComponents()) restyle(child);
        component.repaint();
    }
    static final class Tabs extends BasicTabbedPaneUI {
        protected void installDefaults() { super.installDefaults(); tabInsets = new Insets(8, 12, 8, 12); contentBorderInsets = new Insets(1, 0, 0, 0); }
        protected void paintTabBackground(Graphics g, int placement, int index, int x, int y, int w, int h, boolean active) {
            Graphics2D p = (Graphics2D)g.create(); smooth(p); p.setColor(active ? canvas : surface); p.fillRoundRect(x, y + 2, w, h + 4, 10, 10); p.dispose();
        }
        protected void paintTabBorder(Graphics g, int placement, int index, int x, int y, int w, int h, boolean active) {}
        protected void paintFocusIndicator(Graphics g, int placement, Rectangle[] rects, int index, Rectangle icon, Rectangle text, boolean active) {}
        protected void paintContentBorder(Graphics g, int placement, int selectedIndex) { g.setColor(line); g.drawLine(0, calculateTabAreaHeight(placement, runCount, maxTabHeight), tabPane.getWidth(), calculateTabAreaHeight(placement, runCount, maxTabHeight)); }
    }
    static String cwd(String path) {
        String home = System.getProperty("user.home"); if (path.equals(home)) return "~";
        if (path.startsWith(home + "/")) path = "~" + path.substring(home.length());
        String[] bits = path.replace('\\', '/').split("/");
        if (bits.length > 3) return (path.startsWith("~") ? "~/…/" : "…/") + bits[bits.length - 2] + "/" + bits[bits.length - 1];
        return path;
    }
    static String relative(String iso) {
        try {
            long seconds = Math.max(0, Duration.between(Instant.parse(iso), Instant.now()).getSeconds());
            return seconds < 60 ? "Just now" : seconds < 3600 ? seconds / 60 + "m ago" : seconds < 86400 ? seconds / 3600 + "h ago" : seconds < 604800 ? seconds / 86400 + "d ago" : java.time.format.DateTimeFormatter.ofPattern("MMM d").withZone(ZoneId.systemDefault()).format(Instant.parse(iso));
        } catch (RuntimeException e) { return ""; }
    }
    static JLabel muted(String text) { JLabel l = new JLabel(text); l.putClientProperty("atto.role", "muted"); l.setForeground(muted); l.setFont(body(12)); return l; }
}
