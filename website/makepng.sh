#!/bin/zsh

if [[ $# -ne 1 ]]; then
    echo makepng: expected one arg for output filename
fi

fn=$1

if [[ -f /Users/mmp/capture.png ]]; then 
    ss=~/capture.png
else
    ss=`ls -t ~/Desktop/*.png | head -1`
fi
echo using $ss

convert ${ss} ${fn}-2x.png
convert -scale 50% ${fn}-2x.png ${fn}.png
#rm ${ss}

w=`file ${fn}.png | awk '{print $5}'`
h=`file ${fn}.png | awk '{print $7}' | sed s/,//`

echo '<div class="text-center">'
echo "<img src=\"${fn}.png\" srcset=\"${fn}-2x.png 2x\" width=\"$w\" height=\"$h\">"
echo '</div><br>'

/bin/rm ~/capture*.png
