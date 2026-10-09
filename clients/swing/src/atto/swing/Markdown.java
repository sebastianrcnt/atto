package atto.swing;

import java.util.*;
import java.util.regex.*;

/** A deliberately small, safe Markdown model: no raw HTML or remote images. */
public final class Markdown {
    public record Block(String kind, String text, String language, int level) {}
    private Markdown() {}
    public static List<Block> parse(String text) {
        List<Block> blocks = new ArrayList<>(); StringBuilder paragraph = new StringBuilder(), code = new StringBuilder();
        boolean fenced = false; String language = "", fence = "";
        for (String line : text.split("\n", -1)) {
            String trimmed = line.stripLeading();
            if (trimmed.startsWith("```") || trimmed.startsWith("~~~")) {
                if (!fenced) {
                    flush(blocks, paragraph); fence = trimmed.substring(0, 3);
                    language = trimmed.substring(3).trim(); fenced = true;
                } else if (trimmed.startsWith(fence)) {
                    blocks.add(new Block("code", code.toString(), language, 0)); code.setLength(0); fenced = false;
                } else code.append(line).append('\n');
            } else if (fenced) code.append(line).append('\n');
            else if (line.isBlank()) flush(blocks, paragraph);
            else if (trimmed.matches("#{1,6} .*")) {
                flush(blocks, paragraph); int level = trimmed.indexOf(' ');
                blocks.add(new Block("heading", trimmed.substring(level + 1), "", level));
            } else if (trimmed.matches("(?:[-*+] |[0-9]+[.)] ).*")) {
                flush(blocks, paragraph); blocks.add(new Block("list", trimmed, "", 0));
            } else if (trimmed.startsWith("> ")) {
                flush(blocks, paragraph); blocks.add(new Block("quote", trimmed.substring(2), "", 0));
            } else { if (!paragraph.isEmpty()) paragraph.append('\n'); paragraph.append(line); }
        }
        if (fenced) blocks.add(new Block("code", code.toString(), language, 0));
        flush(blocks, paragraph); return List.copyOf(blocks);
    }
    private static void flush(List<Block> blocks, StringBuilder text) {
        if (!text.isEmpty()) { blocks.add(new Block("paragraph", text.toString(), "", 0)); text.setLength(0); }
    }
    public static String escape(String text) {
        return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace("\"", "&quot;");
    }
    public static boolean safeLink(String url) { return url.startsWith("https://") || url.startsWith("http://") || url.startsWith("mailto:"); }
    public static String inline(String text) {
        StringBuilder html = new StringBuilder();
        Pattern p = Pattern.compile("`([^`]+)`|\\[([^]\\n]+)\\]\\(([^)\\s]+)\\)|\\*\\*([^*]+)\\*\\*|\\*([^*]+)\\*");
        Matcher m = p.matcher(text); int end = 0;
        while (m.find()) {
            html.append(escape(text.substring(end, m.start())));
            if (m.group(1) != null) html.append("<code>").append(escape(m.group(1))).append("</code>");
            else if (m.group(2) != null) {
                if (safeLink(m.group(3))) html.append("<a href=\"").append(escape(m.group(3))).append("\">").append(escape(m.group(2))).append("</a>");
                else html.append(escape(m.group(2)));
            } else if (m.group(4) != null) html.append("<b>").append(escape(m.group(4))).append("</b>");
            else html.append("<i>").append(escape(m.group(5))).append("</i>");
            end = m.end();
        }
        return html.append(escape(text.substring(end))).toString().replace("\n", "<br>");
    }
}
