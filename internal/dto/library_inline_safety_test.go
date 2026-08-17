package dto

import "testing"

// АЛЛОУЛИСТ INLINE — ЕДИНСТВЕННОЕ МЕСТО, ГДЕ ЖИВЁТ XSS-ИСТОРИЯ БИБЛИОТЕКИ, и с Ф7 он охраняет
// НЕаутентифицированный маршрут /api/f/{token}, а не только панель.
//
// Проверяется не «список такой, как записан» — это тавтология, — а два свойства, каждое из которых
// уже один раз оказывалось неверным:
//
//  1. ТИПЫ, КОТОРЫЕ БРАУЗЕР ДОСНИФФЛИВАЕТ ИЛИ ИСПОЛНЯЕТ, НЕ INLINE. Presigned url смотрит в origin
//     бакета, а заголовки ответа принадлежат бакету — поставить `nosniff` нечем. text/plain
//     Chromium досниффливает до text/html, поэтому .txt со <script> исполнился бы на origin бакета
//     у любого, кому прислали публичную ссылку. Он был в списке ровно до тех пор, пока url'ы не
//     покидали панель.
//  2. ПАРАМЕТР MIME НЕ ОТКРЫВАЕТ ОБХОД. Тип приезжает из клиентского заголовка, и `image/svg+xml;
//     charset=utf-8` обязан читаться как svg, а не как незнакомая строка (незнакомая — тоже отказ,
//     но по другой причине, и на неё нельзя опираться).
func TestInlineSafeContentTypes(t *testing.T) {
	for ct, want := range map[string]bool{
		"application/pdf":               true,
		"image/png":                     true,
		"IMAGE/PNG":                     true,
		"image/jpeg; charset=binary":    true,
		"video/mp4":                     true,
		"text/plain":                    false,
		"text/plain; charset=utf-8":     false,
		"TEXT/PLAIN":                    false,
		"text/markdown":                 false,
		"text/html":                     false,
		"image/svg+xml":                 false,
		"image/svg+xml; charset=utf-8":  false,
		"application/xhtml+xml":         false,
		"application/octet-stream":      false,
		"":                              false,
		"application/x-shockwave-flash": false,
	} {
		if got := IsInlineSafeContentType(ct); got != want {
			t.Errorf("IsInlineSafeContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}
