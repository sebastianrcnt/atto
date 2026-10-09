package atto.swing;

import java.io.*;
import java.net.*;
import java.net.http.*;
import java.nio.channels.*;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.*;
import java.util.concurrent.*;
import java.util.function.Consumer;

interface Transport extends AutoCloseable {
    void send(String text) throws Exception;
    void close();

    static Transport open(Options options, Consumer<String> message, Consumer<Throwable> lost) throws Exception {
        if (options.connect().startsWith("ws")) return new Socket(options, message, lost);
        if (options.connect().startsWith("unix://")) {
            SocketChannel channel = SocketChannel.open(StandardProtocolFamily.UNIX);
            try {
                channel.connect(UnixDomainSocketAddress.of(options.connect().substring(7)));
                // Channels streams share a blocking lock: use the socket channel directly
                // so a reader cannot hold the lock needed by the writer.
                return new Lines(new ChannelInput(channel), new ChannelOutput(channel), channel, message, lost);
            } catch (Exception e) { channel.close(); throw e; }
        }
        List<String> args = new ArrayList<>(List.of(options.atto(), "app-server", "--listen", "stdio://"));
        if (options.inProcess()) args.add("--in-process");
        Process p = new ProcessBuilder(args).directory(new File(options.cwd())).redirectError(ProcessBuilder.Redirect.INHERIT).start();
        return new Lines(p.getInputStream(), p.getOutputStream(), () -> {
            p.getOutputStream().close();
            if (!p.waitFor(3, TimeUnit.SECONDS)) p.destroy();
        }, message, lost);
    }

    final class ChannelInput extends InputStream {
        final SocketChannel channel;
        ChannelInput(SocketChannel channel) { this.channel = channel; }
        public int read() throws IOException { byte[] b = new byte[1]; return read(b, 0, 1) < 0 ? -1 : b[0] & 255; }
        public int read(byte[] b, int off, int len) throws IOException { return channel.read(java.nio.ByteBuffer.wrap(b, off, len)); }
    }
    final class ChannelOutput extends OutputStream {
        final SocketChannel channel;
        ChannelOutput(SocketChannel channel) { this.channel = channel; }
        public void write(int b) throws IOException { write(new byte[]{(byte)b}); }
        public void write(byte[] b, int off, int len) throws IOException {
            var buffer = java.nio.ByteBuffer.wrap(b, off, len);
            while (buffer.hasRemaining()) channel.write(buffer);
        }
    }
    final class Lines implements Transport {
        final Writer out; final AutoCloseable resource; volatile boolean closed;
        Lines(InputStream in, OutputStream out, AutoCloseable resource, Consumer<String> message, Consumer<Throwable> lost) {
            this.out = new OutputStreamWriter(out, StandardCharsets.UTF_8); this.resource = resource;
            Thread.ofVirtual().name("atto-reader").start(() -> {
                try (Reader r = new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8.newDecoder().onMalformedInput(java.nio.charset.CodingErrorAction.REPORT).onUnmappableCharacter(java.nio.charset.CodingErrorAction.REPORT)))) {
                    StringBuilder line = new StringBuilder();
                    int c;
                    while ((c = r.read()) != -1) {
                        if (c == '\n') { message.accept(line.toString()); line.setLength(0); }
                        else { line.append((char)c); if (line.length() > 64 * 1024 * 1024) throw new IOException("Message exceeds 64 MiB"); }
                    }
                    if (!closed) lost.accept(new EOFException("atto disconnected"));
                } catch (Throwable e) { if (!closed) lost.accept(e); }
            });
        }
        public synchronized void send(String text) throws IOException { out.write(text); out.write('\n'); out.flush(); }
        public void close() { closed = true; try { resource.close(); } catch (Exception ignored) {} }
    }
    final class Socket implements Transport, WebSocket.Listener {
        final Consumer<String> message; final Consumer<Throwable> lost;
        final StringBuilder text = new StringBuilder(); final WebSocket socket; volatile boolean closed;
        Socket(Options options, Consumer<String> message, Consumer<Throwable> lost) {
            this.message = message; this.lost = lost;
            var builder = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build().newWebSocketBuilder();
            if (!options.token().isEmpty()) builder.header("Authorization", "Bearer " + options.token());
            socket = builder.connectTimeout(Duration.ofSeconds(10)).buildAsync(URI.create(options.connect()), this).join();
        }
        public void onOpen(WebSocket ws) { ws.request(1); }
        public CompletionStage<?> onText(WebSocket ws, CharSequence data, boolean last) {
            text.append(data);
            if (text.length() > 64 * 1024 * 1024) { ws.abort(); lost.accept(new IOException("Message exceeds 64 MiB")); return null; }
            if (last) {
                try { message.accept(text.toString()); } catch (Throwable e) { ws.abort(); lost.accept(e); }
                text.setLength(0);
            }
            ws.request(1); return null;
        }
        public CompletionStage<?> onClose(WebSocket ws, int code, String reason) {
            if (!closed) lost.accept(new EOFException("WebSocket closed (" + code + ")")); return null;
        }
        public void onError(WebSocket ws, Throwable e) { if (!closed) lost.accept(e); }
        public void send(String text) { socket.sendText(text, true).join(); }
        public void close() { closed = true; socket.sendClose(WebSocket.NORMAL_CLOSURE, "detach").orTimeout(2, TimeUnit.SECONDS).exceptionally(e -> { socket.abort(); return null; }); }
    }
}
