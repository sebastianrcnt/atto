package atto.swing;

import java.math.BigDecimal;
import java.util.*;

/** JSON values are maps, lists, strings, booleans, numbers and null. */
public final class Json {
    private Json() {}

    public static Object parse(String text) {
        Parser p = new Parser(text);
        Object value = p.value(0);
        p.space();
        if (p.pos != text.length()) throw p.error("Trailing data");
        return value;
    }

    public static String write(Object value) {
        StringBuilder out = new StringBuilder();
        append(out, value);
        return out.toString();
    }

    private static void append(StringBuilder out, Object v) {
        if (v == null) out.append("null");
        else if (v instanceof String s) {
            out.append('"');
            for (int i = 0; i < s.length(); i++) {
                char c = s.charAt(i);
                switch (c) {
                    case '"' -> out.append("\\\"");
                    case '\\' -> out.append("\\\\");
                    case '\n' -> out.append("\\n");
                    case '\r' -> out.append("\\r");
                    case '\t' -> out.append("\\t");
                    default -> {
                        if (c < 32 || (Character.isSurrogate(c) && !(Character.isHighSurrogate(c)
                                && i + 1 < s.length() && Character.isLowSurrogate(s.charAt(i + 1))))) {
                            out.append(String.format("\\u%04x", (int)c));
                        } else {
                            out.append(c);
                            if (Character.isHighSurrogate(c)) out.append(s.charAt(++i));
                        }
                    }
                }
            }
            out.append('"');
        } else if (v instanceof Boolean) out.append(v);
        else if (v instanceof Number n) {
            if ((n instanceof Double d && !Double.isFinite(d)) || (n instanceof Float f && !Float.isFinite(f)))
                throw new IllegalArgumentException("Non-finite JSON number");
            out.append(n);
        } else if (v instanceof Map<?, ?> map) {
            out.append('{'); boolean first = true;
            for (var e : map.entrySet()) {
                if (!(e.getKey() instanceof String)) throw new IllegalArgumentException("Non-string key");
                if (!first) out.append(','); first = false;
                append(out, e.getKey()); out.append(':'); append(out, e.getValue());
            }
            out.append('}');
        } else if (v instanceof Iterable<?> list) {
            out.append('['); boolean first = true;
            for (Object e : list) { if (!first) out.append(','); first = false; append(out, e); }
            out.append(']');
        } else throw new IllegalArgumentException("Not a JSON value: " + v.getClass());
    }

    @SuppressWarnings("unchecked")
    public static Map<String, Object> obj(Object v) { return v instanceof Map ? (Map<String, Object>)v : Map.of(); }
    @SuppressWarnings("unchecked")
    public static List<Object> list(Object v) { return v instanceof List ? (List<Object>)v : List.of(); }
    public static String str(Object v) { return v == null ? "" : String.valueOf(v); }
    public static long num(Object v) { return v instanceof Number n ? n.longValue() : 0; }
    public static boolean yes(Object v) { return Boolean.TRUE.equals(v); }
    public static Map<String, Object> map(Object... kv) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (int i = 0; i < kv.length; i += 2) result.put((String)kv[i], kv[i + 1]);
        return result;
    }
    static Object freeze(Object value) {
        if (value instanceof Map<?, ?> map) {
            Map<String, Object> result = new LinkedHashMap<>();
            for (var entry : map.entrySet()) result.put((String)entry.getKey(), freeze(entry.getValue()));
            return Collections.unmodifiableMap(result);
        }
        if (value instanceof List<?> list) {
            List<Object> result = new ArrayList<>(); for (Object entry : list) result.add(freeze(entry));
            return Collections.unmodifiableList(result);
        }
        return value;
    }
    public static Object copy(Object value) { return parse(write(value)); }

    private static final class Parser {
        final String text; int pos;
        Parser(String text) { this.text = text; }
        IllegalArgumentException error(String s) { return new IllegalArgumentException(s + " at " + pos); }
        void space() { while (pos < text.length() && " \r\n\t".indexOf(text.charAt(pos)) >= 0) pos++; }
        char take() { if (pos == text.length()) throw error("Unexpected end"); return text.charAt(pos++); }
        Object value(int depth) {
            if (depth > 128) throw error("JSON nesting limit");
            space(); if (pos == text.length()) throw error("Missing value");
            char c = text.charAt(pos);
            if (c == '"') return string();
            if (c == '{') {
                pos++; space(); Map<String, Object> map = new LinkedHashMap<>();
                if (pos < text.length() && text.charAt(pos) == '}') { pos++; return map; }
                while (true) {
                    space(); if (pos == text.length() || text.charAt(pos) != '"') throw error("Expected key");
                    String key = string(); space(); if (take() != ':') throw error("Expected colon");
                    if (map.containsKey(key)) throw error("Duplicate key");
                    map.put(key, value(depth + 1)); space(); char sep = take();
                    if (sep == '}') return map; if (sep != ',') throw error("Expected comma");
                }
            }
            if (c == '[') {
                pos++; space(); List<Object> list = new ArrayList<>();
                if (pos < text.length() && text.charAt(pos) == ']') { pos++; return list; }
                while (true) { list.add(value(depth + 1)); space(); char sep = take();
                    if (sep == ']') return list; if (sep != ',') throw error("Expected comma"); }
            }
            for (String word : List.of("true", "false", "null")) {
                if (text.startsWith(word, pos)) { pos += word.length(); return word.equals("null") ? null : word.equals("true"); }
            }
            int start = pos;
            if (c == '-') pos++;
            if (pos == text.length() || !Character.isDigit(text.charAt(pos))) throw error("Expected value");
            if (text.charAt(pos) == '0') pos++; else digits();
            if (pos < text.length() && text.charAt(pos) == '.') { pos++; digits(); }
            if (pos < text.length() && "eE".indexOf(text.charAt(pos)) >= 0) {
                pos++; if (pos < text.length() && "+-".indexOf(text.charAt(pos)) >= 0) pos++; digits();
            }
            String n = text.substring(start, pos);
            try { return Long.valueOf(n); } catch (NumberFormatException e) { return new BigDecimal(n); }
        }
        void digits() {
            int start = pos; while (pos < text.length() && text.charAt(pos) >= '0' && text.charAt(pos) <= '9') pos++;
            if (pos == start) throw error("Expected digit");
        }
        String string() {
            take(); StringBuilder s = new StringBuilder();
            while (true) {
                char c = take(); if (c == '"') return s.toString();
                if (c < 32) throw error("Control character in string");
                if (c != '\\') { s.append(c); continue; }
                c = take();
                switch (c) {
                    case '"', '\\', '/' -> s.append(c);
                    case 'b' -> s.append('\b'); case 'f' -> s.append('\f');
                    case 'n' -> s.append('\n'); case 'r' -> s.append('\r'); case 't' -> s.append('\t');
                    case 'u' -> {
                        if (pos + 4 > text.length()) throw error("Short unicode escape");
                        for (int i = 0; i < 4; i++) if ("0123456789abcdefABCDEF".indexOf(text.charAt(pos + i)) < 0) throw error("Invalid unicode escape");
                        try { s.append((char)Integer.parseInt(text.substring(pos, pos + 4), 16)); }
                        catch (NumberFormatException e) { throw error("Invalid unicode escape"); }
                        pos += 4;
                    }
                    default -> throw error("Invalid escape");
                }
            }
        }
    }
}
