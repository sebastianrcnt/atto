package atto.swing;

import java.util.*;

public record Options(String atto, String connect, String token, boolean inProcess, String cwd,
                      String session, boolean selftest, String screenshotScript) {
    public static Options parse(String[] args) {
        String atto = "atto", connect = "", token = "", cwd = System.getProperty("user.dir"), session = "";
        boolean inProcess = false, selftest = false; String screenshotScript = "";
        for (int i = 0; i < args.length; i++) {
            switch (args[i]) {
                case "--atto" -> atto = args[++i];
                case "--connect" -> connect = args[++i];
                case "--token" -> token = args[++i];
                case "--cwd" -> cwd = args[++i];
                case "--in-process" -> inProcess = true;
                case "--selftest" -> selftest = true;
                case "--screenshot-script" -> screenshotScript = args[++i];
                default -> {
                    if (args[i].startsWith("-")) throw new IllegalArgumentException("Unknown option " + args[i]);
                    if (!session.isEmpty()) throw new IllegalArgumentException("Only one session argument is allowed");
                    session = args[i];
                }
            }
        }
        if (!connect.isEmpty() && !(connect.startsWith("ws://") || connect.startsWith("wss://") || connect.startsWith("unix:///")))
            throw new IllegalArgumentException("Use ws://, wss:// or unix:/// for --connect");
        return new Options(atto, connect, token, inProcess, cwd, session, selftest, screenshotScript);
    }
}
