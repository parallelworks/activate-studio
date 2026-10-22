"""Sentence embeddings with ONNX Runtime: all-MiniLM-L6-v2, int8, mean pooling.

The backend embed.py and the server's query worker (embed_query.py) use
when onnxruntime, tokenizers and numpy can be imported and the model folder
holds model.onnx and tokenizer.json (fetch_embed.py puts them there).
all-MiniLM-L6-v2 is trained for mean pooling over its tokens; the older
GGUF model through sqlite-lembed takes the first token's state instead,
which ranks passages worse, and costs more than twice the CPU per passage.

Vectors are 384 float32 values, L2-normalized, little-endian: the layout
sqlite-vec's float[384] columns take as a blob. Texts are cut at 256
tokens by default, about the 800 characters of a passage embed.py hands
over. At 128, the length the model was trained at, a passage costs about
40 percent of the CPU and ranks slightly lower; its vectors are a model of
their own (MODEL_ID with a -t128 suffix), so an index never mixes them.
"""
from __future__ import annotations

import sys
from pathlib import Path

MODEL_ID = 'minilm-l6-mean-int8'
MAX_TOKENS = 256
TOKEN_CHOICES = (128, 256)
# 16 texts a batch: as fast as 32, with about 60 percent of the memory.
BATCH = 16
DIMS = 384


def model_id(max_tokens: int = MAX_TOKENS) -> str:
    """The id an index's vectors are marked with, for a truncation length."""
    return MODEL_ID if max_tokens == MAX_TOKENS else f'{MODEL_ID}-t{max_tokens}'


def is_model(mid: str) -> bool:
    """Whether vectors marked mid come from this model (any truncation)."""
    return mid == MODEL_ID or mid.startswith(MODEL_ID + '-t')

# Where fetch_embed.py --deps puts the packages for a clone or a container;
# the macOS app carries them in its own Python instead.
DEPS = Path(__file__).resolve().parent / '.deps'


def _add_deps() -> None:
    if DEPS.is_dir() and str(DEPS) not in sys.path:
        sys.path.insert(0, str(DEPS))


def unavailable(model_dir) -> str | None:
    """Why the backend cannot run here, or None when it can."""
    d = Path(model_dir) if model_dir else None
    if not d or not (d / 'model.onnx').is_file() or not (d / 'tokenizer.json').is_file():
        return f'no ONNX model in {d}'
    _add_deps()
    try:
        import numpy  # noqa: F401
        import onnxruntime  # noqa: F401
        import tokenizers  # noqa: F401
    except ImportError as exc:
        return f'{exc.name or exc} is not installed'
    return None


class Embedder:
    """One loaded model. threads is ONNX Runtime's intra-op thread count:
    the indexer runs several single-threaded embedders side by side, which
    gets through more passages per core than one with many threads."""

    def __init__(self, model_dir, threads: int = 1, max_tokens: int = MAX_TOKENS):
        _add_deps()
        import numpy as np
        import onnxruntime as ort
        from tokenizers import Tokenizer
        self.np = np
        d = Path(model_dir)
        tok = Tokenizer.from_file(str(d / 'tokenizer.json'))
        # The file pads and cuts every text to 128 tokens; pad a batch only
        # to its longest text, and cut where asked.
        tok.enable_truncation(max_tokens)
        tok.enable_padding(pad_id=0, pad_token='[PAD]')
        self.tok = tok
        so = ort.SessionOptions()
        so.intra_op_num_threads = max(1, threads)
        so.inter_op_num_threads = 1
        so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        self.session = ort.InferenceSession(str(d / 'model.onnx'), so, providers=['CPUExecutionProvider'])
        self.inputs = {i.name for i in self.session.get_inputs()}

    def embed(self, texts: list) -> list:
        """One blob per text, in order."""
        np = self.np
        # Texts of like length share a batch, so little of it is padding.
        order = sorted(range(len(texts)), key=lambda i: len(texts[i]))
        out: list = [b''] * len(texts)
        for start in range(0, len(order), BATCH):
            idx = order[start:start + BATCH]
            enc = self.tok.encode_batch([texts[i] or ' ' for i in idx])
            ids = np.array([e.ids for e in enc], dtype=np.int64)
            mask = np.array([e.attention_mask for e in enc], dtype=np.int64)
            feed = {'input_ids': ids, 'attention_mask': mask}
            if 'token_type_ids' in self.inputs:
                feed['token_type_ids'] = np.zeros_like(ids)
            hidden = self.session.run(None, feed)[0]
            m = mask[..., None].astype(np.float32)
            v = (hidden * m).sum(axis=1) / np.maximum(m.sum(axis=1), 1e-9)
            v /= np.maximum(np.linalg.norm(v, axis=1, keepdims=True), 1e-12)
            for row, i in zip(v.astype('<f4'), idx):
                out[i] = row.tobytes()
        return out
