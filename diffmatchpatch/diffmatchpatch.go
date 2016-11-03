// Copyright (c) 2012-2016 The go-diff authors. All rights reserved.
// https://github.com/sergi/go-diff
// See the included LICENSE file for license details.
//
// go-diff is a Go implementation of Google's Diff, Match, and Patch library
// Original library is Copyright (c) 2006 Google Inc.
// http://code.google.com/p/google-diff-match-patch/

// Package diffmatchpatch offers robust algorithms to perform the
// operations required for synchronizing plain text.
package diffmatchpatch

import (
	"bytes"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The data structure representing a diff is an array of tuples:
// [[DiffDelete, 'Hello'], [DiffInsert, 'Goodbye'], [DiffEqual, ' world.']]
// which means: delete 'Hello', add 'Goodbye' and keep ' world.'

// Operation defines the operation of a diff item.
type Operation int8

const (
	// DiffDelete item represents a delete diff.
	DiffDelete Operation = -1
	// DiffInsert item represents an insert diff.
	DiffInsert Operation = 1
	// DiffEqual item represents an equal diff.
	DiffEqual Operation = 0
)

// unescaper unescapes selected chars for compatibility with JavaScript's encodeURI.
// In speed critical applications this could be dropped since the
// receiving application will certainly decode these fine.
// Note that this function is case-sensitive.  Thus "%3F" would not be
// unescaped.  But this is ok because it is only called with the output of
// HttpUtility.UrlEncode which returns lowercase hex.
//
// Example: "%3f" -> "?", "%24" -> "$", etc.
var unescaper = strings.NewReplacer(
	"%21", "!", "%7E", "~", "%27", "'",
	"%28", "(", "%29", ")", "%3B", ";",
	"%2F", "/", "%3F", "?", "%3A", ":",
	"%40", "@", "%26", "&", "%3D", "=",
	"%2B", "+", "%24", "$", "%2C", ",", "%23", "#", "%2A", "*")

// Define some regex patterns for matching boundaries.
var (
	nonAlphaNumericRegex = regexp.MustCompile(`[^a-zA-Z0-9]`)
	whitespaceRegex      = regexp.MustCompile(`\s`)
	linebreakRegex       = regexp.MustCompile(`[\r\n]`)
	blanklineEndRegex    = regexp.MustCompile(`\n\r?\n$`)
	blanklineStartRegex  = regexp.MustCompile(`^\r?\n\r?\n`)
)

func splice(slice []Diff, index int, amount int, elements ...Diff) []Diff {
	return append(slice[:index], append(elements, slice[index+amount:]...)...)
}

// indexOf returns the first index of pattern in str, starting at str[i].
func indexOf(str string, pattern string, i int) int {
	if i > len(str)-1 {
		return -1
	}
	if i <= 0 {
		return strings.Index(str, pattern)
	}
	ind := strings.Index(str[i:], pattern)
	if ind == -1 {
		return -1
	}
	return ind + i
}

// lastIndexOf returns the last index of pattern in str, starting at str[i].
func lastIndexOf(str string, pattern string, i int) int {
	if i < 0 {
		return -1
	}
	if i >= len(str) {
		return strings.LastIndex(str, pattern)
	}
	_, size := utf8.DecodeRuneInString(str[i:])
	return strings.LastIndex(str[:i+size], pattern)
}

// Return the index of pattern in target, starting at target[i].
func runesIndexOf(target, pattern []rune, i int) int {
	if i > len(target)-1 {
		return -1
	}
	if i <= 0 {
		return runesIndex(target, pattern)
	}
	ind := runesIndex(target[i:], pattern)
	if ind == -1 {
		return -1
	}
	return ind + i
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func runesEqual(r1, r2 []rune) bool {
	if len(r1) != len(r2) {
		return false
	}
	for i, c := range r1 {
		if c != r2[i] {
			return false
		}
	}
	return true
}

// The equivalent of strings.Index for rune slices.
func runesIndex(r1, r2 []rune) int {
	last := len(r1) - len(r2)
	for i := 0; i <= last; i++ {
		if runesEqual(r1[i:i+len(r2)], r2) {
			return i
		}
	}
	return -1
}

// Diff represents one diff operation
type Diff struct {
	Type Operation
	Text string
}

// Patch represents one patch operation.
type Patch struct {
	diffs   []Diff
	start1  int
	start2  int
	length1 int
	length2 int
}

// String emulates GNU diff's format.
// Header: @@ -382,8 +481,9 @@
// Indicies are printed as 1-based, not 0-based.
func (p *Patch) String() string {
	var coords1, coords2 string

	if p.length1 == 0 {
		coords1 = strconv.Itoa(p.start1) + ",0"
	} else if p.length1 == 1 {
		coords1 = strconv.Itoa(p.start1 + 1)
	} else {
		coords1 = strconv.Itoa(p.start1+1) + "," + strconv.Itoa(p.length1)
	}

	if p.length2 == 0 {
		coords2 = strconv.Itoa(p.start2) + ",0"
	} else if p.length2 == 1 {
		coords2 = strconv.Itoa(p.start2 + 1)
	} else {
		coords2 = strconv.Itoa(p.start2+1) + "," + strconv.Itoa(p.length2)
	}

	var text bytes.Buffer
	_, _ = text.WriteString("@@ -" + coords1 + " +" + coords2 + " @@\n")

	// Escape the body of the patch with %xx notation.
	for _, aDiff := range p.diffs {
		switch aDiff.Type {
		case DiffInsert:
			_, _ = text.WriteString("+")
		case DiffDelete:
			_, _ = text.WriteString("-")
		case DiffEqual:
			_, _ = text.WriteString(" ")
		}

		_, _ = text.WriteString(strings.Replace(url.QueryEscape(aDiff.Text), "+", " ", -1))
		_, _ = text.WriteString("\n")
	}

	return unescaper.Replace(text.String())
}

// DiffMatchPatch holds the configuration for diff-match-patch operations.
type DiffMatchPatch struct {
	// Number of seconds to map a diff before giving up (0 for infinity).
	DiffTimeout time.Duration
	// Cost of an empty edit operation in terms of edit characters.
	DiffEditCost int
	// How far to search for a match (0 = exact location, 1000+ = broad match).
	// A match this many characters away from the expected location will add
	// 1.0 to the score (0.0 is a perfect match).
	MatchDistance int
	// When deleting a large block of text (over ~64 characters), how close do
	// the contents have to be to match the expected contents. (0.0 = perfection,
	// 1.0 = very loose).  Note that MatchThreshold controls how closely the
	// end points of a delete need to match.
	PatchDeleteThreshold float64
	// Chunk size for context length.
	PatchMargin int
	// The number of bits in an int.
	MatchMaxBits int
	// At what point is no match declared (0.0 = perfection, 1.0 = very loose).
	MatchThreshold float64
}

// New creates a new DiffMatchPatch object with default parameters.
func New() *DiffMatchPatch {
	// Defaults.
	return &DiffMatchPatch{
		DiffTimeout:          time.Second,
		DiffEditCost:         4,
		MatchThreshold:       0.5,
		MatchDistance:        1000,
		PatchDeleteThreshold: 0.5,
		PatchMargin:          4,
		MatchMaxBits:         32,
	}
}

//  MATCH FUNCTIONS

// MatchMain locates the best instance of 'pattern' in 'text' near 'loc'.
// Returns -1 if no match found.
func (dmp *DiffMatchPatch) MatchMain(text, pattern string, loc int) int {
	// Check for null inputs not needed since null can't be passed in C#.

	loc = int(math.Max(0, math.Min(float64(loc), float64(len(text)))))
	if text == pattern {
		// Shortcut (potentially not guaranteed by the algorithm)
		return 0
	} else if len(text) == 0 {
		// Nothing to match.
		return -1
	} else if loc+len(pattern) <= len(text) && text[loc:loc+len(pattern)] == pattern {
		// Perfect match at the perfect spot!  (Includes case of null pattern)
		return loc
	}
	// Do a fuzzy compare.
	return dmp.MatchBitap(text, pattern, loc)
}

// MatchBitap locates the best instance of 'pattern' in 'text' near 'loc' using the
// Bitap algorithm.  Returns -1 if no match found.
func (dmp *DiffMatchPatch) MatchBitap(text, pattern string, loc int) int {
	// Initialise the alphabet.
	s := dmp.MatchAlphabet(pattern)

	// Highest score beyond which we give up.
	scoreThreshold := dmp.MatchThreshold
	// Is there a nearby exact match? (speedup)
	bestLoc := indexOf(text, pattern, loc)
	if bestLoc != -1 {
		scoreThreshold = math.Min(dmp.matchBitapScore(0, bestLoc, loc,
			pattern), scoreThreshold)
		// What about in the other direction? (speedup)
		bestLoc = lastIndexOf(text, pattern, loc+len(pattern))
		if bestLoc != -1 {
			scoreThreshold = math.Min(dmp.matchBitapScore(0, bestLoc, loc,
				pattern), scoreThreshold)
		}
	}

	// Initialise the bit arrays.
	matchmask := 1 << uint((len(pattern) - 1))
	bestLoc = -1

	var binMin, binMid int
	binMax := len(pattern) + len(text)
	lastRd := []int{}
	for d := 0; d < len(pattern); d++ {
		// Scan for the best match; each iteration allows for one more error.
		// Run a binary search to determine how far from 'loc' we can stray at
		// this error level.
		binMin = 0
		binMid = binMax
		for binMin < binMid {
			if dmp.matchBitapScore(d, loc+binMid, loc, pattern) <= scoreThreshold {
				binMin = binMid
			} else {
				binMax = binMid
			}
			binMid = (binMax-binMin)/2 + binMin
		}
		// Use the result from this iteration as the maximum for the next.
		binMax = binMid
		start := int(math.Max(1, float64(loc-binMid+1)))
		finish := int(math.Min(float64(loc+binMid), float64(len(text))) + float64(len(pattern)))

		rd := make([]int, finish+2)
		rd[finish+1] = (1 << uint(d)) - 1

		for j := finish; j >= start; j-- {
			var charMatch int
			if len(text) <= j-1 {
				// Out of range.
				charMatch = 0
			} else if _, ok := s[text[j-1]]; !ok {
				charMatch = 0
			} else {
				charMatch = s[text[j-1]]
			}

			if d == 0 {
				// First pass: exact match.
				rd[j] = ((rd[j+1] << 1) | 1) & charMatch
			} else {
				// Subsequent passes: fuzzy match.
				rd[j] = ((rd[j+1]<<1)|1)&charMatch | (((lastRd[j+1] | lastRd[j]) << 1) | 1) | lastRd[j+1]
			}
			if (rd[j] & matchmask) != 0 {
				score := dmp.matchBitapScore(d, j-1, loc, pattern)
				// This match will almost certainly be better than any existing
				// match.  But check anyway.
				if score <= scoreThreshold {
					// Told you so.
					scoreThreshold = score
					bestLoc = j - 1
					if bestLoc > loc {
						// When passing loc, don't exceed our current distance from loc.
						start = int(math.Max(1, float64(2*loc-bestLoc)))
					} else {
						// Already passed loc, downhill from here on in.
						break
					}
				}
			}
		}
		if dmp.matchBitapScore(d+1, loc, loc, pattern) > scoreThreshold {
			// No hope for a (better) match at greater error levels.
			break
		}
		lastRd = rd
	}
	return bestLoc
}

// matchBitapScore computes and returns the score for a match with e errors and x location.
func (dmp *DiffMatchPatch) matchBitapScore(e, x, loc int, pattern string) float64 {
	accuracy := float64(e) / float64(len(pattern))
	proximity := math.Abs(float64(loc - x))
	if dmp.MatchDistance == 0 {
		// Dodge divide by zero error.
		if proximity == 0 {
			return accuracy
		}

		return 1.0
	}
	return accuracy + (proximity / float64(dmp.MatchDistance))
}

// MatchAlphabet initialises the alphabet for the Bitap algorithm.
func (dmp *DiffMatchPatch) MatchAlphabet(pattern string) map[byte]int {
	s := map[byte]int{}
	charPattern := []byte(pattern)
	for _, c := range charPattern {
		_, ok := s[c]
		if !ok {
			s[c] = 0
		}
	}
	i := 0

	for _, c := range charPattern {
		value := s[c] | int(uint(1)<<uint((len(pattern)-i-1)))
		s[c] = value
		i++
	}
	return s
}

//  PATCH FUNCTIONS

// PatchAddContext increases the context until it is unique,
// but doesn't let the pattern expand beyond MatchMaxBits.
func (dmp *DiffMatchPatch) PatchAddContext(patch Patch, text string) Patch {
	if len(text) == 0 {
		return patch
	}

	pattern := text[patch.start2 : patch.start2+patch.length1]
	padding := 0

	// Look for the first and last matches of pattern in text.  If two
	// different matches are found, increase the pattern length.
	for strings.Index(text, pattern) != strings.LastIndex(text, pattern) &&
		len(pattern) < dmp.MatchMaxBits-2*dmp.PatchMargin {
		padding += dmp.PatchMargin
		maxStart := max(0, patch.start2-padding)
		minEnd := min(len(text), patch.start2+patch.length1+padding)
		pattern = text[maxStart:minEnd]
	}
	// Add one chunk for good luck.
	padding += dmp.PatchMargin

	// Add the prefix.
	prefix := text[max(0, patch.start2-padding):patch.start2]
	if len(prefix) != 0 {
		patch.diffs = append([]Diff{Diff{DiffEqual, prefix}}, patch.diffs...)
	}
	// Add the suffix.
	suffix := text[patch.start2+patch.length1 : min(len(text), patch.start2+patch.length1+padding)]
	if len(suffix) != 0 {
		patch.diffs = append(patch.diffs, Diff{DiffEqual, suffix})
	}

	// Roll back the start points.
	patch.start1 -= len(prefix)
	patch.start2 -= len(prefix)
	// Extend the lengths.
	patch.length1 += len(prefix) + len(suffix)
	patch.length2 += len(prefix) + len(suffix)

	return patch
}

// PatchMake computes a list of patches.
func (dmp *DiffMatchPatch) PatchMake(opt ...interface{}) []Patch {
	if len(opt) == 1 {
		diffs, _ := opt[0].([]Diff)
		text1 := dmp.DiffText1(diffs)
		return dmp.PatchMake(text1, diffs)
	} else if len(opt) == 2 {
		text1 := opt[0].(string)
		switch t := opt[1].(type) {
		case string:
			diffs := dmp.DiffMain(text1, t, true)
			if len(diffs) > 2 {
				diffs = dmp.DiffCleanupSemantic(diffs)
				diffs = dmp.DiffCleanupEfficiency(diffs)
			}
			return dmp.PatchMake(text1, diffs)
		case []Diff:
			return dmp.patchMake2(text1, t)
		}
	} else if len(opt) == 3 {
		return dmp.PatchMake(opt[0], opt[2])
	}
	return []Patch{}
}

// patchMake2 computes a list of patches to turn text1 into text2.
// text2 is not provided, diffs are the delta between text1 and text2.
func (dmp *DiffMatchPatch) patchMake2(text1 string, diffs []Diff) []Patch {
	// Check for null inputs not needed since null can't be passed in C#.
	patches := []Patch{}
	if len(diffs) == 0 {
		return patches // Get rid of the null case.
	}

	patch := Patch{}
	charCount1 := 0 // Number of characters into the text1 string.
	charCount2 := 0 // Number of characters into the text2 string.
	// Start with text1 (prepatchText) and apply the diffs until we arrive at
	// text2 (postpatchText). We recreate the patches one by one to determine
	// context info.
	prepatchText := text1
	postpatchText := text1

	for i, aDiff := range diffs {
		if len(patch.diffs) == 0 && aDiff.Type != DiffEqual {
			// A new patch starts here.
			patch.start1 = charCount1
			patch.start2 = charCount2
		}

		switch aDiff.Type {
		case DiffInsert:
			patch.diffs = append(patch.diffs, aDiff)
			patch.length2 += len(aDiff.Text)
			postpatchText = postpatchText[:charCount2] +
				aDiff.Text + postpatchText[charCount2:]
		case DiffDelete:
			patch.length1 += len(aDiff.Text)
			patch.diffs = append(patch.diffs, aDiff)
			postpatchText = postpatchText[:charCount2] + postpatchText[charCount2+len(aDiff.Text):]
		case DiffEqual:
			if len(aDiff.Text) <= 2*dmp.PatchMargin &&
				len(patch.diffs) != 0 && i != len(diffs)-1 {
				// Small equality inside a patch.
				patch.diffs = append(patch.diffs, aDiff)
				patch.length1 += len(aDiff.Text)
				patch.length2 += len(aDiff.Text)
			}
			if len(aDiff.Text) >= 2*dmp.PatchMargin {
				// Time for a new patch.
				if len(patch.diffs) != 0 {
					patch = dmp.PatchAddContext(patch, prepatchText)
					patches = append(patches, patch)
					patch = Patch{}
					// Unlike Unidiff, our patch lists have a rolling context.
					// http://code.google.com/p/google-diff-match-patch/wiki/Unidiff
					// Update prepatch text & pos to reflect the application of the
					// just completed patch.
					prepatchText = postpatchText
					charCount1 = charCount2
				}
			}
		}

		// Update the current character count.
		if aDiff.Type != DiffInsert {
			charCount1 += len(aDiff.Text)
		}
		if aDiff.Type != DiffDelete {
			charCount2 += len(aDiff.Text)
		}
	}

	// Pick up the leftover patch if not empty.
	if len(patch.diffs) != 0 {
		patch = dmp.PatchAddContext(patch, prepatchText)
		patches = append(patches, patch)
	}

	return patches
}

// PatchDeepCopy returns an array that is identical to a
// given an array of patches.
func (dmp *DiffMatchPatch) PatchDeepCopy(patches []Patch) []Patch {
	patchesCopy := []Patch{}
	for _, aPatch := range patches {
		patchCopy := Patch{}
		for _, aDiff := range aPatch.diffs {
			patchCopy.diffs = append(patchCopy.diffs, Diff{
				aDiff.Type,
				aDiff.Text,
			})
		}
		patchCopy.start1 = aPatch.start1
		patchCopy.start2 = aPatch.start2
		patchCopy.length1 = aPatch.length1
		patchCopy.length2 = aPatch.length2
		patchesCopy = append(patchesCopy, patchCopy)
	}
	return patchesCopy
}

// PatchApply merges a set of patches onto the text.  Returns a patched text, as well
// as an array of true/false values indicating which patches were applied.
func (dmp *DiffMatchPatch) PatchApply(patches []Patch, text string) (string, []bool) {
	if len(patches) == 0 {
		return text, []bool{}
	}

	// Deep copy the patches so that no changes are made to originals.
	patches = dmp.PatchDeepCopy(patches)

	nullPadding := dmp.PatchAddPadding(patches)
	text = nullPadding + text + nullPadding
	patches = dmp.PatchSplitMax(patches)

	x := 0
	// delta keeps track of the offset between the expected and actual
	// location of the previous patch.  If there are patches expected at
	// positions 10 and 20, but the first patch was found at 12, delta is 2
	// and the second patch has an effective expected position of 22.
	delta := 0
	results := make([]bool, len(patches))
	for _, aPatch := range patches {
		expectedLoc := aPatch.start2 + delta
		text1 := dmp.DiffText1(aPatch.diffs)
		var startLoc int
		endLoc := -1
		if len(text1) > dmp.MatchMaxBits {
			// PatchSplitMax will only provide an oversized pattern
			// in the case of a monster delete.
			startLoc = dmp.MatchMain(text, text1[:dmp.MatchMaxBits], expectedLoc)
			if startLoc != -1 {
				endLoc = dmp.MatchMain(text,
					text1[len(text1)-dmp.MatchMaxBits:], expectedLoc+len(text1)-dmp.MatchMaxBits)
				if endLoc == -1 || startLoc >= endLoc {
					// Can't find valid trailing context.  Drop this patch.
					startLoc = -1
				}
			}
		} else {
			startLoc = dmp.MatchMain(text, text1, expectedLoc)
		}
		if startLoc == -1 {
			// No match found.  :(
			results[x] = false
			// Subtract the delta for this failed patch from subsequent patches.
			delta -= aPatch.length2 - aPatch.length1
		} else {
			// Found a match.  :)
			results[x] = true
			delta = startLoc - expectedLoc
			var text2 string
			if endLoc == -1 {
				text2 = text[startLoc:int(math.Min(float64(startLoc+len(text1)), float64(len(text))))]
			} else {
				text2 = text[startLoc:int(math.Min(float64(endLoc+dmp.MatchMaxBits), float64(len(text))))]
			}
			if text1 == text2 {
				// Perfect match, just shove the Replacement text in.
				text = text[:startLoc] + dmp.DiffText2(aPatch.diffs) + text[startLoc+len(text1):]
			} else {
				// Imperfect match.  Run a diff to get a framework of equivalent
				// indices.
				diffs := dmp.DiffMain(text1, text2, false)
				if len(text1) > dmp.MatchMaxBits && float64(dmp.DiffLevenshtein(diffs))/float64(len(text1)) > dmp.PatchDeleteThreshold {
					// The end points match, but the content is unacceptably bad.
					results[x] = false
				} else {
					diffs = dmp.DiffCleanupSemanticLossless(diffs)
					index1 := 0
					for _, aDiff := range aPatch.diffs {
						if aDiff.Type != DiffEqual {
							index2 := dmp.DiffXIndex(diffs, index1)
							if aDiff.Type == DiffInsert {
								// Insertion
								text = text[:startLoc+index2] + aDiff.Text + text[startLoc+index2:]
							} else if aDiff.Type == DiffDelete {
								// Deletion
								startIndex := startLoc + index2
								text = text[:startIndex] +
									text[startIndex+dmp.DiffXIndex(diffs, index1+len(aDiff.Text))-index2:]
							}
						}
						if aDiff.Type != DiffDelete {
							index1 += len(aDiff.Text)
						}
					}
				}
			}
		}
		x++
	}
	// Strip the padding off.
	text = text[len(nullPadding) : len(nullPadding)+(len(text)-2*len(nullPadding))]
	return text, results
}

// PatchAddPadding adds some padding on text start and end so that edges can match something.
// Intended to be called only from within patchApply.
func (dmp *DiffMatchPatch) PatchAddPadding(patches []Patch) string {
	paddingLength := dmp.PatchMargin
	nullPadding := ""
	for x := 1; x <= paddingLength; x++ {
		nullPadding += string(x)
	}

	// Bump all the patches forward.
	for i := range patches {
		patches[i].start1 += paddingLength
		patches[i].start2 += paddingLength
	}

	// Add some padding on start of first diff.
	if len(patches[0].diffs) == 0 || patches[0].diffs[0].Type != DiffEqual {
		// Add nullPadding equality.
		patches[0].diffs = append([]Diff{Diff{DiffEqual, nullPadding}}, patches[0].diffs...)
		patches[0].start1 -= paddingLength // Should be 0.
		patches[0].start2 -= paddingLength // Should be 0.
		patches[0].length1 += paddingLength
		patches[0].length2 += paddingLength
	} else if paddingLength > len(patches[0].diffs[0].Text) {
		// Grow first equality.
		extraLength := paddingLength - len(patches[0].diffs[0].Text)
		patches[0].diffs[0].Text = nullPadding[len(patches[0].diffs[0].Text):] + patches[0].diffs[0].Text
		patches[0].start1 -= extraLength
		patches[0].start2 -= extraLength
		patches[0].length1 += extraLength
		patches[0].length2 += extraLength
	}

	// Add some padding on end of last diff.
	last := len(patches) - 1
	if len(patches[last].diffs) == 0 || patches[last].diffs[len(patches[last].diffs)-1].Type != DiffEqual {
		// Add nullPadding equality.
		patches[last].diffs = append(patches[last].diffs, Diff{DiffEqual, nullPadding})
		patches[last].length1 += paddingLength
		patches[last].length2 += paddingLength
	} else if paddingLength > len(patches[last].diffs[len(patches[last].diffs)-1].Text) {
		// Grow last equality.
		lastDiff := patches[last].diffs[len(patches[last].diffs)-1]
		extraLength := paddingLength - len(lastDiff.Text)
		patches[last].diffs[len(patches[last].diffs)-1].Text += nullPadding[:extraLength]
		patches[last].length1 += extraLength
		patches[last].length2 += extraLength
	}

	return nullPadding
}

// PatchSplitMax looks through the patches and breaks up any which are longer than the
// maximum limit of the match algorithm.
// Intended to be called only from within patchApply.
func (dmp *DiffMatchPatch) PatchSplitMax(patches []Patch) []Patch {
	patchSize := dmp.MatchMaxBits
	for x := 0; x < len(patches); x++ {
		if patches[x].length1 <= patchSize {
			continue
		}
		bigpatch := patches[x]
		// Remove the big old patch.
		patches = append(patches[:x], patches[x+1:]...)
		x--

		start1 := bigpatch.start1
		start2 := bigpatch.start2
		precontext := ""
		for len(bigpatch.diffs) != 0 {
			// Create one of several smaller patches.
			patch := Patch{}
			empty := true
			patch.start1 = start1 - len(precontext)
			patch.start2 = start2 - len(precontext)
			if len(precontext) != 0 {
				patch.length1 = len(precontext)
				patch.length2 = len(precontext)
				patch.diffs = append(patch.diffs, Diff{DiffEqual, precontext})
			}
			for len(bigpatch.diffs) != 0 && patch.length1 < patchSize-dmp.PatchMargin {
				diffType := bigpatch.diffs[0].Type
				diffText := bigpatch.diffs[0].Text
				if diffType == DiffInsert {
					// Insertions are harmless.
					patch.length2 += len(diffText)
					start2 += len(diffText)
					patch.diffs = append(patch.diffs, bigpatch.diffs[0])
					bigpatch.diffs = bigpatch.diffs[1:]
					empty = false
				} else if diffType == DiffDelete && len(patch.diffs) == 1 && patch.diffs[0].Type == DiffEqual && len(diffText) > 2*patchSize {
					// This is a large deletion.  Let it pass in one chunk.
					patch.length1 += len(diffText)
					start1 += len(diffText)
					empty = false
					patch.diffs = append(patch.diffs, Diff{diffType, diffText})
					bigpatch.diffs = bigpatch.diffs[1:]
				} else {
					// Deletion or equality.  Only take as much as we can stomach.
					diffText = diffText[:min(len(diffText), patchSize-patch.length1-dmp.PatchMargin)]

					patch.length1 += len(diffText)
					start1 += len(diffText)
					if diffType == DiffEqual {
						patch.length2 += len(diffText)
						start2 += len(diffText)
					} else {
						empty = false
					}
					patch.diffs = append(patch.diffs, Diff{diffType, diffText})
					if diffText == bigpatch.diffs[0].Text {
						bigpatch.diffs = bigpatch.diffs[1:]
					} else {
						bigpatch.diffs[0].Text =
							bigpatch.diffs[0].Text[len(diffText):]
					}
				}
			}
			// Compute the head context for the next patch.
			precontext = dmp.DiffText2(patch.diffs)
			precontext = precontext[max(0, len(precontext)-dmp.PatchMargin):]

			postcontext := ""
			// Append the end context for this patch.
			if len(dmp.DiffText1(bigpatch.diffs)) > dmp.PatchMargin {
				postcontext = dmp.DiffText1(bigpatch.diffs)[:dmp.PatchMargin]
			} else {
				postcontext = dmp.DiffText1(bigpatch.diffs)
			}

			if len(postcontext) != 0 {
				patch.length1 += len(postcontext)
				patch.length2 += len(postcontext)
				if len(patch.diffs) != 0 && patch.diffs[len(patch.diffs)-1].Type == DiffEqual {
					patch.diffs[len(patch.diffs)-1].Text += postcontext
				} else {
					patch.diffs = append(patch.diffs, Diff{DiffEqual, postcontext})
				}
			}
			if !empty {
				x++
				patches = append(patches[:x], append([]Patch{patch}, patches[x:]...)...)
			}
		}
	}
	return patches
}

// PatchToText takes a list of patches and returns a textual representation.
func (dmp *DiffMatchPatch) PatchToText(patches []Patch) string {
	var text bytes.Buffer
	for _, aPatch := range patches {
		_, _ = text.WriteString(aPatch.String())
	}
	return text.String()
}

// PatchFromText parses a textual representation of patches and returns a List of Patch
// objects.
func (dmp *DiffMatchPatch) PatchFromText(textline string) ([]Patch, error) {
	patches := []Patch{}
	if len(textline) == 0 {
		return patches, nil
	}
	text := strings.Split(textline, "\n")
	textPointer := 0
	patchHeader := regexp.MustCompile("^@@ -(\\d+),?(\\d*) \\+(\\d+),?(\\d*) @@$")

	var patch Patch
	var sign uint8
	var line string
	for textPointer < len(text) {

		if !patchHeader.MatchString(text[textPointer]) {
			return patches, errors.New("Invalid patch string: " + text[textPointer])
		}

		patch = Patch{}
		m := patchHeader.FindStringSubmatch(text[textPointer])

		patch.start1, _ = strconv.Atoi(m[1])
		if len(m[2]) == 0 {
			patch.start1--
			patch.length1 = 1
		} else if m[2] == "0" {
			patch.length1 = 0
		} else {
			patch.start1--
			patch.length1, _ = strconv.Atoi(m[2])
		}

		patch.start2, _ = strconv.Atoi(m[3])

		if len(m[4]) == 0 {
			patch.start2--
			patch.length2 = 1
		} else if m[4] == "0" {
			patch.length2 = 0
		} else {
			patch.start2--
			patch.length2, _ = strconv.Atoi(m[4])
		}
		textPointer++

		for textPointer < len(text) {
			if len(text[textPointer]) > 0 {
				sign = text[textPointer][0]
			} else {
				textPointer++
				continue
			}

			line = text[textPointer][1:]
			line = strings.Replace(line, "+", "%2b", -1)
			line, _ = url.QueryUnescape(line)
			if sign == '-' {
				// Deletion.
				patch.diffs = append(patch.diffs, Diff{DiffDelete, line})
			} else if sign == '+' {
				// Insertion.
				patch.diffs = append(patch.diffs, Diff{DiffInsert, line})
			} else if sign == ' ' {
				// Minor equality.
				patch.diffs = append(patch.diffs, Diff{DiffEqual, line})
			} else if sign == '@' {
				// Start of next patch.
				break
			} else {
				// WTF?
				return patches, errors.New("Invalid patch mode '" + string(sign) + "' in: " + string(line))
			}
			textPointer++
		}

		patches = append(patches, patch)
	}
	return patches, nil
}
