"""Vectors from indexer/embed_onnx.py for a list of texts, for
internal/embed's comparison test.

  PYTHONPATH=indexer/.deps python3 -I scripts/embed_vectors.py MODEL_DIR IN_JSONL OUT_JSONL [MAX_TOKENS] [LIMIT]

IN_JSONL has a "text" per line (scripts/wordpiece_samples.py's output
serves); OUT_JSONL gets {"text", "v"} with v the base64 blob.
"""
import base64
import json
import os
import sys

here = os.path.dirname(os.path.abspath(__file__))
sys.path[:0] = [os.path.join(here, '..', '..', 'indexer')] + [p for p in os.environ.get('PYTHONPATH', '').split(':') if p]
import embed_onnx  # noqa: E402

max_tokens = int(sys.argv[4]) if len(sys.argv) > 4 else 256
limit = int(sys.argv[5]) if len(sys.argv) > 5 else 3000
texts = []
with open(sys.argv[2]) as f:
    for line in f:
        texts.append(json.loads(line)['text'])
        if len(texts) >= limit:
            break
e = embed_onnx.Embedder(sys.argv[1], threads=1, max_tokens=max_tokens)
blobs = e.embed(texts)
with open(sys.argv[3], 'w') as out:
    for t, b in zip(texts, blobs):
        out.write(json.dumps({'text': t, 'v': base64.b64encode(b).decode()}) + '\n')
print(len(texts))
