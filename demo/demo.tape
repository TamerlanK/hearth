Output demo/demo.gif

Set Shell bash
Set FontSize 13
Set Width 1200
Set Height 560
Set Framerate 12
Set TypingSpeed 35ms
Set Theme "Catppuccin Mocha"
Set Padding 8

Hide
Type "bin/hearth serve --addr 127.0.0.1:4000 --log-level error >/dev/null 2>&1 &"
Enter
Type "tmux -f /dev/null new-session -d -s hearth -x 170 -y 34 'bin/hearth connect 127.0.0.1:4000 --name alice'"
Enter
Type "tmux set -g status off; tmux split-window -h -t hearth 'bin/hearth connect 127.0.0.1:4000 --name bob'; tmux select-pane -t hearth:0.0"
Enter
Sleep 800ms
Type "clear; tmux attach -t hearth"
Enter
Sleep 1s
Show

Type "hey bob, welcome to hearth"
Enter
Sleep 700ms

Ctrl+B
Right
Sleep 300ms
Type "hi alice! one binary, server and client"
Enter
Sleep 700ms
Type "/join golang"
Enter
Sleep 900ms

Ctrl+B
Left
Sleep 300ms
Type "/msg bob see you in #golang"
Enter
Sleep 900ms
Type "/join golang"
Enter
Sleep 700ms
Type "rooms, history replay, DMs"
Enter
Sleep 900ms

Tab
Sleep 300ms
Type "?"
Sleep 2500ms
Escape
Sleep 1200ms

Hide
Ctrl+C
Sleep 300ms
Type "tmux kill-server; kill %1"
Enter
