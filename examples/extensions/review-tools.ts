export default function (atto: Atto) {
  atto.ui.render({ site: "toolCall" }, async (e, next) => {
    const { Box, Text } = atto.ui.resolve(e);
    const original = await next(e);
    return Box({ children: [original, Text({
      text: "Reviewed by my extension", color: "muted",
    })].filter(x => x !== null) });
  });
}
