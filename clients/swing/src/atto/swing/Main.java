package atto.swing;

import javax.swing.*;
import java.awt.*;

public final class Main {
    public static void main(String[] args) throws Exception {
        Options options;
        try {
            if (args.length == 0) {
                Settings settings = new Settings(); String connect = settings.string("connection", "");
                options = Options.parse(connect.isEmpty() ? new String[]{"--atto", settings.string("atto", "atto")} : new String[]{"--connect", connect});
            } else options = Options.parse(args);
        }
        catch (RuntimeException e) {
            System.err.println(e.getMessage());
            System.err.println("Usage: java -jar atto-swing.jar [--atto PATH] [--connect ws://…|unix:///…] [--token T] [--in-process] [--cwd DIR] [session]");
            System.exit(2); return;
        }
        if (options.selftest()) { SelfTest.run(options); return; }
        if (GraphicsEnvironment.isHeadless()) throw new IllegalStateException("A display is required (or use --selftest)");
        System.setProperty("apple.laf.useScreenMenuBar", "true");
        System.setProperty("apple.awt.application.appearance", "system");
        System.setProperty("apple.awt.application.name", "atto");
        UIManager.setLookAndFeel(UIManager.getSystemLookAndFeelClassName());
        SwingUtilities.invokeLater(() -> new Desktop(options).show());
    }
}
