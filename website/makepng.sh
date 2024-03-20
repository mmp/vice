#!/bin/zsh

if [[ $# -ne 1 ]]; then
    echo makevideo: expected one arg for output filename
fi

fn=$1
ss=`ls -t ~/Desktop/Screenshot* | head -1`

mv ${ss} ${fn}-2x.png
convert -scale 50% ${fn}-2x.png ${fn}.png

w=`file ${fn}.png | awk '{print $5}'`
h=`file ${fn}.png | awk '{print $7}' | sed s/,//`

echo '<div class="text-center">'
echo "<img src=\"${fn}.png\" srcset=\"${fn}-2x.png 2x\" width=\"$w\" height=\"$h\">"
echo '</div>'


