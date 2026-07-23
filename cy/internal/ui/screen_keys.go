package ui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

func keyIsCtrl(msg tea.KeyPressMsg, base rune) bool {
	key := msg.Key()
	if key.Mod&tea.ModCtrl == 0 {
		return false
	}
	return keyLayoutBase(msg) == base
}

func keyLayoutBase(msg tea.KeyPressMsg) rune {
	key := msg.Key()
	for _, r := range []rune{key.BaseCode, key.Code, key.ShiftedCode} {
		if mapped := layoutBaseRune(r); mapped != 0 {
			return mapped
		}
	}
	for _, r := range key.Text {
		return layoutBaseRune(r)
	}
	return 0
}

func layoutBaseRune(r rune) rune {
	if r == 0 {
		return 0
	}
	r = unicode.ToLower(r)
	if mapped, ok := russianKeyBase[r]; ok {
		return mapped
	}
	return r
}

var russianKeyBase = map[rune]rune{
	'ё': '`',
	'й': 'q',
	'ц': 'w',
	'у': 'e',
	'к': 'r',
	'е': 't',
	'н': 'y',
	'г': 'u',
	'ш': 'i',
	'щ': 'o',
	'з': 'p',
	'х': '[',
	'ъ': ']',
	'ф': 'a',
	'ы': 's',
	'в': 'd',
	'а': 'f',
	'п': 'g',
	'р': 'h',
	'о': 'j',
	'л': 'k',
	'д': 'l',
	'ж': ';',
	'э': '\'',
	'я': 'z',
	'ч': 'x',
	'с': 'c',
	'м': 'v',
	'и': 'b',
	'т': 'n',
	'ь': 'm',
	'б': ',',
	'ю': '.',
}
