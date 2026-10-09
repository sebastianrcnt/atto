#!/bin/sh
set -eu
cd "$(dirname "$0")"
mkdir -p build/classes build/tests
find src -name '*.java' > build/sources
javac --release 21 -encoding UTF-8 -d build/classes @build/sources
jar --create --file build/atto-swing.jar --main-class atto.swing.Main -C build/classes .
if [ "${1:-}" = test ]; then
  find test -name '*.java' > build/test-sources
  javac --release 21 -encoding UTF-8 -cp build/classes -d build/tests @build/test-sources
  sep=:; if [ "${OS:-}" = Windows_NT ]; then sep=';'; fi
  java -Djava.awt.headless=true -cp "build/classes${sep}build/tests" atto.swing.Tests
fi
