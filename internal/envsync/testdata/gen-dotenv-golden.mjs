// Regenerates dotenv-16.3.1-golden.json: what the REAL dotenv 16.3.1 parse()
// returns for each source in the corpus below. dotenv_test.go checks its Go
// port of that parser against this file, so the port stays faithful.
//
// dotenv 16.3.1 is the copy wrangler 4.149.0 bundles and reads .dev.vars
// with. Not run by CI (the repo has no JS lane); run it by hand when the
// corpus or the wrangler pin changes:
//
//   mkdir -p /tmp/dotenv && cd /tmp/dotenv && npm pack dotenv@16.3.1 && tar xzf dotenv-16.3.1.tgz
//   node internal/envsync/testdata/gen-dotenv-golden.mjs /tmp/dotenv/package \
//     > internal/envsync/testdata/dotenv-16.3.1-golden.json
import { createRequire } from 'node:module'
import path from 'node:path'

const pkgDir = process.argv[2]
if (!pkgDir) {
  console.error('usage: node gen-dotenv-golden.mjs <path to the unpacked dotenv package>')
  process.exit(2)
}
const require = createRequire(import.meta.url)
const dotenv = require(path.resolve(pkgDir, 'lib/main.js'))
const version = require(path.resolve(pkgDir, 'package.json')).version
if (version !== '16.3.1') {
  console.error(`expected dotenv 16.3.1, got ${version}`)
  process.exit(1)
}

const corpus = [
  ['bare', 'A=1\nB=hello world\n'],
  ['bare-trailing-comment', 'A=value # comment\nB=x#y\n'],
  ['bare-trimmed', 'A=   padded   \n'],
  ['empty', 'A=\nB=2\n'],
  ['empty-then-quote-line', "A=\n'x'\nB=2\n"],
  ['export-and-colon', 'export A=1\nB: 2\nC:3\n'],
  ['spaces-around-equals', 'A = 1\n  B\t=\t2\n'],
  ['comments-and-blanks', '# c\n\n  # indented\nA=1\n\n\nB=2\n'],
  ['crlf', 'A=1\r\nB="x\r\ny"\r\nC=3\r'],
  ['single-literal', "A='a\\nb\\tc\\\\d \"q\" `b` # h'\n"],
  ['single-multiline', "A='line1\nline2\n'\nB=2\n"],
  ['single-padded', "A='  x  '\n"],
  ['single-backslash-quote', "A='a\\'b'\nB=2\n"],
  ['single-trailing-backslash', "A='a\\'\nB='x'\nC=3\n"],
  ['single-trailing-backslash-swallow', "A='a\\'\nB=x'\nC=3\n"],
  ['single-unterminated', "A='abc\nB=2\n"],
  ['double-escapes', 'A="a\\nb\\rc\\td\\\\e\\"f"\n'],
  ['double-escaped-backslash-n', 'A="x\\\\ny"\n'],
  ['double-multiline', 'A="l1\nl2"\nB=2\n'],
  ['double-hash', 'A="a # b"\n'],
  ['backtick', 'A=`it\'s "x" \\n`\nB=2\n'],
  ['backtick-multiline', 'A=`l1\nl2`\nB=2\n'],
  ['quote-start-bare', "A='abc\nB=it's\n"],
  ['mismatched-quotes', 'A=\'abc"\nB="x\'\n'],
  ['unicode', 'A=naïve café 日本 🎉\nB=\'naïve café\'\n'],
  ['unicode-spaces', 'A=\u00a0x\u3000\nB=\'\u00a0x\u3000\'\n'],
  ['line-separator-bare', 'A=x\u2028B=2\nC=3\n'],
  ['line-separator-quoted', "A='x\u2028B=2'\nC=3\n"],
  ['line-separator-double', 'A="x\u2029y"\nC=3\n'],
  ['duplicate-key', 'A=1\nA=2\n'],
  ['equals-in-value', 'A=a=b==c\n'],
  ['tab-bare', 'A=a\tb\n'],
  ['inner-quote-lines', "A='a'\n'b'\nC=3\n"],
  ['injection-single', "A='x\nKEYTO_DEV_USER=0\nEVIL=1'\nKEYTO_DEV_USER=1\n"],
  ['injection-double-escaped', 'A="x\\nKEYTO_DEV_USER=0"\nKEYTO_DEV_USER=1\n'],
  ['dotted-key', 'a.b-c=1\n'],
  ['no-trailing-newline', 'A=1\nB=\'x\''],
  // What env sync renders for devvars_roundtrip_test.go's tricky and
  // unwritable values, plus a local overrides section.
  ['rendered-dev-vars', "# ============================================================\n# Managed by keyto env sync. Edits above the local overrides line are lost.\n# Re-run: keyto env sync\n# Local overrides: end this file with the line \"# local overrides below\"\n# and put your own KEY=value lines under it; env sync keeps them.\n# KEYTO_* keys are set by the platform and never belong here.\n# ============================================================\n\n# Secrets from uat (fetched via Hub)\n# NOT WRITTEN: ALL_QUOTES (its value can't be written so wrangler reads it back unchanged: it contains both ' and `, and also \" or a literal \\n or \\r)\nBACKTICK='run `ls`'\nBS_NEWLINE=\"a\\\\nb\"\n# NOT WRITTEN: BS_N_QUOTES (its value can't be written so wrangler reads it back unchanged: it contains both ' and `, and also \" or a literal \\n or \\r)\nCRLF=\"one\\r\\ntwo\"\n# NOT WRITTEN: CR_AND_QUOTE (its value can't be written so wrangler reads it back unchanged: it has a carriage return as well as a \" or a literal \\n or \\r)\nCR_ONLY=\"one\\rtwo\"\nDOUBLE_QUOTE='say \"hi\"'\nEMPTY=''\nEQUALS=a=b==c\nESCAPED_QUOTES='\\\"x\\\"'\nHASH='abc#def # not a comment'\n# NOT WRITTEN: HASH_BS (its value can't be written so wrangler reads it back unchanged: it ends with a backslash and needs quotes)\nINJECTION=\"x\\nKEYTO_DEV_USER=0\\nEVIL=1\"\nJSON_KEY='{\"type\":\"service_account\",\"private_key\":\"-----BEGIN PRIVATE KEY-----\\nMIIEvQ\\n-----END PRIVATE KEY-----\\n\",\"client_email\":\"x@y.iam\"}'\nLINE_SEP=\"x\u2028y\u2029z\"\nLITERAL_BS_N=literal \\n and \\r stay\nMARKERISH='# local overrides below, but not the line'\n# NOT WRITTEN: MARKER_LINE (its value can't be written so wrangler reads it back unchanged: it contains the line \"# local overrides below\")\nNEWLINES=\"line one\\nline two\\n\"\nPRETTY_JSON='{\n  \"a\": \"b\\nc\"\n}'\nQUOTED_START=`'starts with a quote`\nSINGLE_QUOTE=`it's`\nSPACES='  padded  '\nTAB=a\tb\nTRAILING_BS=ends with \\\nTWO_QUOTES=`it's \"quoted\"`\nUNICODE=na\u00efve caf\u00e9 \u65e5\u672c \ud83c\udf89\nUNICODE_SPACES='\u00a0x\u3000'\nWIN_PATH=C:\\new\\temp\nWRAPPED='\"wrapped\"'\n\n# Signs you in as the local dev user (@hemfrid/workers-auth).\nKEYTO_DEV_USER=1\n\n# local overrides below\nMINE=1\n"],
]

const out = corpus.map(([name, src]) => ({ name, src, parsed: dotenv.parse(src) }))
process.stdout.write(JSON.stringify(out, null, 2) + '\n')
