#!/bin/zsh

#ffmpeg -i "$1" -b:v 0 -crf 25 -f mp4 -vcodec libx264 -pix_fmt yuv420p $2.mp4
#ffmpeg -i "$1" -c vp9 -b:v 0 -crf 41 $2.webm
#echo "<video autoplay loop muted playsinline>"
#echo "  <source src=\"$2.webm\" type=\"video/webm\">"
#echo "  <source src=\"$2.mp4\" type=\"video/mp4\">"
#echo "</video>"

x2=${2%%.gif}-2x.gif

ffmpeg -i "$1" -pix_fmt rgb24 -r 5 -f gif - | gifsicle --optimize=3 --delay=20 >| $x2
ffmpeg -i "$1" -pix_fmt rgb24 -r 5 -f gif - | gifsicle --optimize=3 --scale=0.5 --delay=20 >| $2

w=`file $2 | awk '{print $7}'`
h=`file $2 | awk '{print $9}'`

echo '<div class="text-center">'
echo "<img src=\"$2\" srcset=\"$x2 2x\" width=\"$w\" height=\"$h\" class=\"img-fluid\">"
echo '</div>'
