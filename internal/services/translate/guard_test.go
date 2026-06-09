package translate

import "testing"

func TestLooksLikeTargetLang(t *testing.T) {
	cases := []struct {
		name string
		code string
		text string
		want bool
	}{
		{"ar got chinese (the bug)", "ar", "辟邪多股菩提 蜜桃花开手链 – 自然种子饰品，助您情绪绽放与情感和谐连接", false},
		{"th got chinese (the bug)", "th", "菩提山桃花菩提多股手链 – 自然种子珠宝促进情绪繁荣与和谐联系", false},
		{"ar real arabic", "ar", "سوار بوذا متعدد الخيوط بأزهار الخوخ – مجوهرات بذور طبيعية", true},
		{"th real thai", "th", "สร้อยข้อมือโพธิ์ดอกท้อหลายเส้น – เครื่องประดับเมล็ดธรรมชาติ", true},
		{"ar with latin brand name", "ar", "غطاء iPhone 15 الواقي", true},
		{"ru real russian", "ru", "Браслет Бодхи с цветами персика", true},
		{"latin target skips check", "es", "Pulsera de Buda con flor de durazno", true},
		{"empty", "ar", "", false},
	}
	for _, c := range cases {
		if got := looksLikeTargetLang(c.code, c.text); got != c.want {
			t.Errorf("%s: looksLikeTargetLang(%q, ...) = %v, want %v", c.name, c.code, got, c.want)
		}
	}
}
