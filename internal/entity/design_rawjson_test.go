package entity

import (
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// RAWJSON ОБЯЗАН ВЕСТИ СЕБЯ КАК json.RawMessage, ПОТОМУ ЧТО ЗАМЕНЯЕТ ЕГО.
//
// Заменён он был не по вкусу: json.RawMessage не реализует sql.Scanner, а database/sql кладёт NULL
// только в Scanner, *[]byte или *any — поэтому КАЖДОЕ чтение таблицы с NULLable JSON-колонкой
// падало на любой настоящей базе. Найдено исполнением против MySQL в контейнере; ни одна
// чисто-логическая проба этого не видит, потому что тип корректен и в памяти безупречен.
func TestRawJSONScansEveryFormTheDriverGives(t *testing.T) {
	// Три формы, в которых MySQL-драйвер отдаёт JSON-колонку. NULL — та самая, на которой падало.
	var r RawJSON
	require.NoError(t, r.Scan(nil))
	require.Nil(t, r, "NULL обязан читаться пустотой, а не ошибкой")

	require.NoError(t, r.Scan([]byte(`{"a":1}`)))
	require.JSONEq(t, `{"a":1}`, string(r))

	require.NoError(t, r.Scan(`{"b":2}`))
	require.JSONEq(t, `{"b":2}`, string(r))
}

// БУФЕР ДРАЙВЕРА КОПИРУЕТСЯ, А НЕ ЗАПОМИНАЕТСЯ ССЫЛКОЙ. Он переиспользуется между строками, и
// сохранённая ссылка показала бы содержимое СЛЕДУЮЩЕЙ строки — молча и только на многострочных
// чтениях, то есть ровно там, где это труднее всего заметить.
func TestRawJSONCopiesTheDriverBuffer(t *testing.T) {
	buf := []byte(`{"row":1}`)
	var r RawJSON
	require.NoError(t, r.Scan(buf))

	copy(buf, []byte(`{"row":9}`)) // драйвер переиспользовал буфер под следующую строку

	require.JSONEq(t, `{"row":1}`, string(r), "значение обязано пережить переиспользование буфера")
}

// ПУСТОТА ЕДЕТ В БАЗУ КАК NULL, а не как пустая строка: колонки объявлены `JSON NULL`, и пустая
// строка не является валидным JSON — MySQL отверг бы её.
func TestRawJSONWritesEmptyAsNull(t *testing.T) {
	var empty RawJSON
	v, err := empty.Value()
	require.NoError(t, err)
	require.Equal(t, driver.Value(nil), v)

	full := RawJSON(`{"a":1}`)
	v, err = full.Value()
	require.NoError(t, err)
	require.NotNil(t, v)
}

// СЕРИАЛИЗУЕТСЯ СЫРЫМИ БАЙТАМИ, А НЕ BASE64. Голый именованный []byte печатается как base64, и
// первый, кто сложит структуру в снапшот или лог, получил бы нечитаемое молча: JSON остался бы
// валидным, а содержимое — недоступным всем, кто его ждёт.
func TestRawJSONMarshalsRawNotBase64(t *testing.T) {
	out, err := json.Marshal(struct {
		Payload RawJSON `json:"payload"`
	}{Payload: RawJSON(`{"a":1}`)})
	require.NoError(t, err)
	require.JSONEq(t, `{"payload":{"a":1}}`, string(out),
		"base64 здесь означал бы валидный JSON с нечитаемым содержимым")

	var back struct {
		Payload RawJSON `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(out, &back))
	require.JSONEq(t, `{"a":1}`, string(back.Payload), "круговой рейс обязан вернуть то же")
}
