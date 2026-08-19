#!/usr/bin/env python3
"""Derive the turnkey CFD training workflow from deploy/workflow.yaml.

Prints a yaml where the makau configurations preset is baked in as the
plain form defaults: scheduler placement on, container source, Gemma 4
serve, starter corpus. Push the output to the platform record (local
type) per deploy/COMPUTE.md's stored-yaml procedure.

    make_cfd_training_yaml.py [workflow.yaml] [--shared-dir DIR]

--shared-dir points the images, model, and starter corpus at one
read-only copy on the resource, so a class of trainees launches without
each pulling 8 GB of images and 59 GB of model weights. The directory
holds containers/studio.sif, containers/vllm.sif,
models/<model dir name>, and cfd-starter-kb.tar.gz.
"""
import sys
import yaml

argv = sys.argv[1:]
shared = None
if '--shared-dir' in argv:
    i = argv.index('--shared-dir')
    shared = argv[i + 1].rstrip('/')
    del argv[i:i + 2]
src = argv[0] if argv else 'deploy/workflow.yaml'
d = yaml.safe_load(open(src))

ins = d['on']['execute']['inputs']
preset = d.pop('configurations')['makau']['inputs']

for group, values in preset.items():
    for key, val in values.items():
        ins[group]['items'][key]['default'] = val

# The form opens compact: the scheduler group carries the two fields a
# first launch needs; everything else expands on demand.
for group, item in ins.items():
    if isinstance(item, dict) and item.get('type') == 'group':
        item['collapsed'] = group not in ('resource_and_execution', 'slurm')

ins['slurm']['items']['scheduler_directives']['default'] = '--gres=gpu:h100_sxm5:4'

if shared:
    model_name = ins['model_settings']['items']['model_dir']['default'].rsplit('/', 1)[-1]
    ins['app_settings']['items']['image_path']['default'] = shared + '/containers/studio.sif'
    ins['model_settings']['items']['vllm_image']['default'] = shared + '/containers/vllm.sif'
    ins['model_settings']['items']['model_dir']['default'] = shared + '/models/' + model_name
    ins['kb_settings']['items']['starter_bundle']['default'] = shared + '/cfd-starter-kb.tar.gz'
ins['session_settings']['items']['session_name']['tooltip'] = (
    'Platform session for the web interface, per user, kept across relaunches. '
    'Change it to run more than one studio.')

yaml.safe_dump(d, sys.stdout, sort_keys=False, width=100, allow_unicode=True)
