package rux

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The rux T-rex: the Rex icon's dinosaur head, back spikes and all, drawn in dots that are
// big where the light falls and small in shadow, in the icon's orange, pink and violet.
var trexMask = strings.Split(strings.Trim(`
             ##############
         ######################
    ##  #########################
   #### ###########################
    #######  ########################
   ########  #########################
    ####################################
   ######################################
    ################
   ##############   ###################
    ############   ###################
   #############   #################
    ###################
   ##################
    ################`, "\n"), "\n")

// trexLogo renders the T-rex with the light at phase (radians), so it can shimmer.
func trexLogo(phase float64) string {
	w := 0
	for _, r := range trexMask {
		w = max(w, len(r))
	}
	h := len(trexMask)
	dots := []rune{' ', '·', '·', '•', '●'}
	stops := [][3]float64{{0xff, 0x9a, 0x3c}, {0xf4, 0x72, 0xb6}, {0xa7, 0x8b, 0xfa}}
	var b strings.Builder
	for y, row := range trexMask {
		for x := 0; x < w; x++ {
			if x >= len(row) || row[x] == ' ' {
				b.WriteByte(' ')
				continue
			}
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			light := 1 - (0.65*fx + 0.55*fy) + 0.12*math.Sin(phase-fx*5)
			lvl := max(1, min(4, int(light*5.2)))
			// Orange at the top left, through pink, to violet at the bottom right.
			t := math.Max(0, math.Min(1, 0.55*fx+0.45*fy))
			var c [3]float64
			if t < 0.5 {
				c = mix(stops[0], stops[1], t*2)
			} else {
				c = mix(stops[1], stops[2], (t-0.5)*2)
			}
			col := lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", int(c[0]), int(c[1]), int(c[2])))
			b.WriteString(lipgloss.NewStyle().Foreground(col).Render(string(dots[lvl])))
		}
		if y < h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func mix(a, b [3]float64, t float64) [3]float64 {
	return [3]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}
