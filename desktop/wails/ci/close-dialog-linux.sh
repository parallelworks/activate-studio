#!/bin/bash
# Closes the shell's folder dialog as a person pressing Escape would. No
# window manager runs under Xvfb, so focus is set on the dialog directly.
w=$(timeout 10 xdotool search --sync --limit 1 --name 'Choose the folder') || { echo "no dialog window found"; exit 1; }
xdotool windowfocus "$w"
sleep 0.3
xdotool key --clearmodifiers Escape
echo "closed window $w"
