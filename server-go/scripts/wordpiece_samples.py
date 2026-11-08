"""Token ids from the tokenizers library for a sample of texts, for
internal/embed/wordpiece's comparison test.

  PYTHONPATH=indexer/.deps python3 -I scripts/wordpiece_samples.py TOKENIZER_JSON OUT_JSONL [TEXT_DIR...]

Takes every file under each TEXT_DIR, cut as embed.py cuts passages
(1,000 characters, 120 overlap) and whole, plus random strings over many
scripts, punctuation, controls and the special tokens, and writes one
{"text", "ids"} line per text, cut at 256 tokens as embed_onnx.py does.
"""
import json
import os
import random
import sys

sys.path[:0] = [p for p in os.environ.get('PYTHONPATH', '').split(':') if p]
from tokenizers import Tokenizer  # noqa: E402

tok = Tokenizer.from_file(sys.argv[1])
tok.enable_truncation(256)
tok.no_padding()
texts = []
for d in sys.argv[3:]:
    for root, _, files in os.walk(d):
        for f in sorted(files):
            try:
                t = open(os.path.join(root, f), encoding='utf-8', errors='replace').read()
            except OSError:
                continue
            texts.append(t[:20000])
            i = 0
            while i < len(t) and i < 30000:
                texts.append(t[i:i + 1000])
                i += 880
rnd = random.Random(7)
pools = [range(0x20, 0x7F), range(0xA0, 0x250), range(0x300, 0x370), range(0x370, 0x400), range(0x400, 0x530),
         range(0x590, 0x700), range(0x900, 0xE00), range(0x1100, 0x1200), range(0x2000, 0x2BFF), range(0x3000, 0x3100),
         range(0x4E00, 0x4F00), range(0xAC00, 0xAD00), range(0xFE00, 0xFFFF), range(0x1F300, 0x1FAFF), range(0x0, 0x20),
         range(0x10000, 0x10400), range(0x20000, 0x20100)]
words = ['[CLS]', '[SEP]', '[MASK]', '[PAD]', '[UNK]', '[cls]', '[MAS', 'hello', 'naïve', 'Ünïcödé', 'İstanbul', 'straße',
         'ﬁnancial', '½', '①', 'x²', 'café', '𝐁𝐨𝐥𝐝', 'ＦＵＬＬ', '​', '﻿', '­', 'é', 'Å', 'ǅ']
for n in range(20000):
    parts = []
    for _ in range(rnd.randint(1, 60)):
        r = rnd.random()
        if r < 0.15:
            parts.append(rnd.choice(words))
        elif r < 0.3:
            parts.append(' ' * rnd.randint(1, 3))
        else:
            pool = rnd.choice(pools)
            parts.append(''.join(chr(rnd.choice(pool)) for _ in range(rnd.randint(1, 12))))
    s = ''.join(parts)
    s = ''.join(c for c in s if not 0xD800 <= ord(c) <= 0xDFFF)
    texts.append(s)
texts.append('x' * 150)
texts.append('ab' * 60 + ' ' + 'word ' * 400)
texts.append('')
texts.append(' ')
with open(sys.argv[2], 'w') as out:
    for t in texts:
        out.write(json.dumps({'text': t, 'ids': tok.encode(t).ids}) + '\n')
print(len(texts))
