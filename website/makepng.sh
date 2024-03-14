#!/bin/zsh

x2=${2%%.png}-2x.png
convert "$1" $x2
convert "$1" -scale 50% $2

w=`file $2 | awk '{print $5}'`
h=`file $2 | awk '{print $7}' | sed s/,//`

echo '<div class="text-center">'
echo "<img src=\"$2\" srcset=\"$x2 2x\" width=\"$w\" height=\"$h\">"
echo '</div>'


