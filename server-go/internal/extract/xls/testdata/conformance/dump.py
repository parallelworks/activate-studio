"""Print the BIFF records of an .xls: dump.py FILE [RECORD_NAME...]"""
import sys, struct
from xlrd import compdoc
NAMES={0x809:'BOF',0x0a:'EOF',0x06:'FORMULA',0x207:'STRING',0x3c:'CONTINUE',0x4bc:'SHRFMLA',0x221:'ARRAY',0x203:'NUMBER',0x27e:'RK',0xbd:'MULRK',0xfd:'LABELSST',0x204:'LABEL',0x205:'BOOLERR',0x201:'BLANK',0xbe:'MULBLANK',0xfc:'SST',0x41e:'FORMAT',0xe0:'XF',0x85:'BOUNDSHEET',0x22:'DATEMODE',0x42:'CODEPAGE',0xd6:'RSTRING',0x208:'ROW',0x200:'DIMENSIONS'}
data=open(sys.argv[1],'rb').read()
cd=compdoc.CompDoc(data)
for name in ('Workbook','Book'):
    try:
        s=cd.get_named_stream(name)
        if s: break
    except Exception: s=None
pos=0; only=set(sys.argv[2:])
while pos+4<=len(s):
    rc,ln=struct.unpack('<HH',s[pos:pos+4]); body=s[pos+4:pos+4+ln]; pos+=4+ln
    n=NAMES.get(rc,hex(rc))
    if not only or n in only: print(n, ln, body[:48].hex())
