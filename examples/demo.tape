# Run from the repository root with: task record-demo
# Uses ntrecord (VHS-compatible .tape runner with native Kitty graphics support).
Output dist/demo.gif
Output dist/demo.txt

Set Shell /bin/sh
Env ENV ""
Env PS1 "demo> "
Env COLORTERM "truecolor"
Env CLICOLOR_FORCE "1"
Set FontFamily "Go Mono"
Set FontSize 16
Set Columns 120
Set Rows 34
Set Theme "Catppuccin Mocha"
Set Padding 16
Set Margin 10
Set MarginFill "#0b0f17"
Set WindowBar Colorful
Set BorderRadius 10
Set Framerate 10
Set TypingSpeed 60ms
Set WaitTimeout 15s

# Launch prebuilt subgraphs demo while hidden so no shell prompt appears.
Hide
Type@1ms "./bin/ntnodegraph-subgraphs"
Enter
Wait+Screen /FLATTENED GRAPH/
Sleep 800ms
Type "0"
Sleep 800ms
Show

# 1. Root composable pipeline: graph/* components, rosette Kitty preview & Flatten() pane.
Screenshot dist/demo.png
Sleep 2s

# 2. Drill inside synth (graph/svg-critic-loop@1) to inspect boundary ports & feedback loop.
Enter
Wait+Screen /Drilled into synth/
Sleep 2.2s

# 3. Return to root graph, expand canvas full-width, open Inspector, and toggle Grid/Vector.
Backspace
Wait+Screen /Returned to composable-studio/
Sleep 1.2s
Type "y"
Sleep 800ms
Type "0"
Sleep 800ms
Type "i"
Sleep 1.3s
Type "v"
Wait+Screen /Canvas mode: grid/
Sleep 1.4s
Type "v"
Wait+Screen /Canvas mode: vector/
Sleep 400ms
Type "0"
Sleep 1.4s

Hide
Type "q"
Sleep 300ms
