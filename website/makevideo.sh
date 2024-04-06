#!/bin/zsh

if [[ $# -ne 1 ]]; then
    echo makevideo: expected one arg for output filename
fi

fn=$1
rec=`ls -t ~/Desktop/Screen\ Rec* | head -1`

ffmpeg -i "${rec}" -pix_fmt rgb24 -r 5 -f gif - | gifsicle --optimize=3 --delay=20 >| ${fn}-2x.gif
ffmpeg -i "${rec}" -pix_fmt rgb24 -r 5 -f gif - | gifsicle --optimize=3 --scale=0.5 --delay=20 >| ${fn}.gif

w=`file ${fn}.gif | awk '{print $7}'`
h=`file ${fn}.gif | awk '{print $9}'`

echo '<div class="text-center">'
echo "<img src=\"${fn}.gif\" srcset=\"${fn}-2x.gif 2x\" width=\"$w\" height=\"$h\" class=\"img-fluid\">"
echo '</div>'
