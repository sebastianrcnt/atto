// Copy to ~/.atto/extensions/; atto.d.ts there supplies the global API types.
export default function (atto: Atto) {
  let count = 0;
  atto.on("session_start", async () => {
    count = (await atto.store.get<number>("count")) ?? 0;
    atto.ui.invalidate({ site: "pane", id: "counter" });
  });
  atto.ui.render({ site: "pane", id: "counter" }, e => {
    const { Box, Text, Button } = atto.ui.resolve(e);
    return <Box gap={1}>
      <Text text={`Count: ${count}`} />
      <Button key="more" label="Add one" hotkey="a" onPress={async () => {
        count++;
        await atto.store.set("count", count);
        atto.ui.invalidate({ site: "pane", id: "counter" });
      }} />
    </Box>;
  });
  atto.registerCommand("counter", { handler: () =>
    atto.ui.open({ site: "pane", id: "counter", title: "Counter", focus: true }) });
}
