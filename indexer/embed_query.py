#!/usr/bin/env python3
"""Query vectors for search by meaning, from one loaded ONNX model.

The server (server/src/embedWorker.ts) starts this once and keeps it, so
the model loads once rather than on every search. It speaks JSON lines:

  on start   {"ready": true, "model": "minilm-l6-mean-int8"}, or
             {"ready": false, "error": "..."} and exit 1
  request    {"id": 1, "texts": ["turbine blade fatigue"]}
  reply      {"id": 1, "vectors": ["<base64>"]}, each 384 float32 values,
             little-endian, L2-normalized, or {"id": 1, "error": "..."}

It exits when its input closes.
"""
import argparse
import base64
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))


def send(obj) -> None:
    sys.stdout.write(json.dumps(obj) + '\n')
    sys.stdout.flush()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--onnx', required=True, help='folder with model.onnx and tokenizer.json')
    args = ap.parse_args()
    # One short text at a time: one thread is enough, and the tokenizer and
    # numpy need none of their own.
    os.environ.update(TOKENIZERS_PARALLELISM='false', OMP_NUM_THREADS='1', OPENBLAS_NUM_THREADS='1')
    try:
        import embed_onnx
        why = embed_onnx.unavailable(args.onnx)
        if why:
            raise RuntimeError(why)
        embedder = embed_onnx.Embedder(args.onnx, threads=1)
    except Exception as exc:
        send({'ready': False, 'error': str(exc)})
        return 1
    send({'ready': True, 'model': embed_onnx.MODEL_ID})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        rid = None
        try:
            req = json.loads(line)
            rid = req.get('id')
            texts = [str(t)[:2000] for t in req.get('texts', [])]
            vectors = [base64.b64encode(b).decode('ascii') for b in embedder.embed(texts)]
            send({'id': rid, 'vectors': vectors})
        except Exception as exc:
            send({'id': rid, 'error': str(exc)})
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
