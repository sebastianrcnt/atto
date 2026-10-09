package atto.swing;

import java.nio.file.*;
import java.util.*;
import static atto.swing.Json.*;

public final class Settings {
    final Path file = Path.of(System.getProperty("user.home"), ".atto", "swing.json");
    public Map<String, Object> values = new LinkedHashMap<>();
    public Settings() {
        try { if (Files.exists(file)) values.putAll(obj(parse(Files.readString(file)))); }
        catch (Exception e) { System.err.println("swing settings: " + e.getMessage()); }
    }
    public int integer(String key, int fallback) { return values.containsKey(key) ? (int)num(values.get(key)) : fallback; }
    public String string(String key, String fallback) { return values.containsKey(key) ? str(values.get(key)) : fallback; }
    public void save(Map<String, Object> snapshot) {
        try {
            Files.createDirectories(file.getParent());
            Path temporary = Files.createTempFile(file.getParent(), "swing-", ".json");
            try {
                Files.writeString(temporary, write(snapshot));
                try { Files.move(temporary, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE); }
                catch (AtomicMoveNotSupportedException e) { Files.move(temporary, file, StandardCopyOption.REPLACE_EXISTING); }
            } finally { Files.deleteIfExists(temporary); }
        } catch (Exception e) { System.err.println("swing settings: " + e.getMessage()); }
    }
}
