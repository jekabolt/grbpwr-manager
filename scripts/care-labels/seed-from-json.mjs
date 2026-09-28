#!/usr/bin/env node
// Печатает детерминированный SQL-блок сида переводов волокон для составников (care labels) из
// fiber-translations.json — единственного источника истины переводов. Руками блок сида в миграции
// не правится: поменялся JSON → перегенерировать блок и вклеить между маркерами.
//
//   node scripts/care-labels/seed-from-json.mjs [path/to/fiber-translations.json]
//       → SQL-блок в stdout
//   node scripts/care-labels/seed-from-json.mjs --check internal/store/sql/NNNN_fiber_label_translation.sql [json]
//       → код 1, если блок между маркерами в миграции не совпадает побайтно со сгенерированным
//
// По умолчанию JSON берётся из scripts/care-labels/fiber-translations.json (в репозитории) относительно корня
// репозитория.
//
// Форма строки — INSERT IGNORE … SELECT … FROM fiber WHERE code = …, по строке на пару
// (волокно, язык): код из JSON, которого нет в базе (на проде может не быть ELA), даёт ноль строк —
// без ошибки FK и без сироты. IGNORE — чтобы перезапуск после падения посреди файла и уже
// отредактированные в админке переводы не ломали миграцию и не затирались.

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Закрытый список языков ленты — тот же, что CHECK в миграции и entity.LabelLangs. Порядок = порядок
// строк на ленте.
const LABEL_LANGS = ['en', 'fr', 'de', 'it', 'es', 'pt', 'nl', 'pl', 'cn', 'jp'];
const FIBER_CODE_RE = /^[A-Z0-9]{1,8}$/;
const NAME_MAX = 64; // fiber_label_translation.name VARCHAR(64) — символы, не байты

const BEGIN = '-- BEGIN generated seed: scripts/care-labels/seed-from-json.mjs (do not edit by hand)';
const END = '-- END generated seed';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const defaultJSON = resolve(repoRoot, 'scripts', 'care-labels', 'fiber-translations.json');

function fail(msg) {
  process.stderr.write(`seed-from-json: ${msg}\n`);
  process.exit(2);
}

// Строковый литерал MySQL: одинарные кавычки (ANSI_QUOTES на кластере делает двойные
// идентификатором), обратный слэш экранируется, т.к. NO_BACKSLASH_ESCAPES не включён.
function lit(s) {
  return `'${s.replace(/\\/g, '\\\\').replace(/'/g, "''")}'`;
}

function build(data) {
  if (JSON.stringify(data.languages) !== JSON.stringify(LABEL_LANGS)) {
    fail(`languages must be exactly ${LABEL_LANGS.join(',')} in that order, got ${JSON.stringify(data.languages)}`);
  }
  if (!Array.isArray(data.fibers) || data.fibers.length === 0) fail('fibers is empty');

  const seen = new Set();
  const inserts = [];
  const animal = [];
  for (const f of data.fibers) {
    if (!FIBER_CODE_RE.test(f.code ?? '')) fail(`bad fibre code ${JSON.stringify(f.code)}`);
    if (seen.has(f.code)) fail(`duplicate fibre code ${f.code}`);
    seen.add(f.code);
    if (typeof f.non_textile_animal !== 'boolean') fail(`${f.code}: non_textile_animal must be boolean`);
    const keys = Object.keys(f.names ?? {});
    const extra = keys.filter((k) => !LABEL_LANGS.includes(k));
    if (extra.length) fail(`${f.code}: unknown label languages ${extra.join(',')}`);
    for (const lang of LABEL_LANGS) {
      const name = (f.names?.[lang] ?? '').trim();
      if (!name) fail(`${f.code}: missing ${lang}`);
      if ([...name].length > NAME_MAX) fail(`${f.code}/${lang}: longer than ${NAME_MAX} characters`);
      inserts.push(
        `INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) ` +
          `SELECT f.code, ${lit(lang)}, ${lit(name)} FROM fiber f WHERE f.code = ${lit(f.code)};`,
      );
    }
    if (f.non_textile_animal) animal.push(f.code);
  }

  const lines = [
    BEGIN,
    `-- ${data.fibers.length} fibre codes x ${LABEL_LANGS.length} label languages = ${inserts.length} candidate rows,`,
    '-- a code the database does not know is skipped by the SELECT, never an FK error.',
    ...inserts,
  ];
  if (animal.length) {
    lines.push(
      `UPDATE fiber SET animal_non_textile = 1 WHERE code IN (${animal.map(lit).join(', ')});`,
    );
  }
  lines.push(END);
  return lines.join('\n') + '\n';
}

const args = process.argv.slice(2);
let checkPath = null;
if (args[0] === '--check') {
  checkPath = args[1];
  if (!checkPath) fail('--check needs a migration path');
  args.splice(0, 2);
}
const jsonPath = args[0] ? resolve(args[0]) : defaultJSON;

let data;
try {
  data = JSON.parse(readFileSync(jsonPath, 'utf8'));
} catch (e) {
  fail(`cannot read ${jsonPath}: ${e.message}`);
}
const block = build(data);

if (!checkPath) {
  process.stdout.write(block);
  process.exit(0);
}

const sql = readFileSync(checkPath, 'utf8');
const from = sql.indexOf(BEGIN);
const to = sql.indexOf(END);
if (from < 0 || to < 0 || to < from) fail(`${checkPath}: seed markers not found`);
const embedded = sql.slice(from, to + END.length) + '\n';
if (embedded !== block) {
  process.stderr.write(`seed-from-json: ${checkPath}: embedded seed differs from ${jsonPath}; regenerate\n`);
  process.exit(1);
}
process.stdout.write(`seed-from-json: ${checkPath} matches ${jsonPath} (${block.split('\n').filter((l) => l.startsWith('INSERT')).length} rows)\n`);
