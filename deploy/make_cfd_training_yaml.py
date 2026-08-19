#!/usr/bin/env python3
"""Derive the turnkey CFD training workflow from deploy/workflow.yaml.

Prints a yaml where the makau configurations preset is baked in as the
plain form defaults: scheduler placement on, container source, Gemma 4
serve, starter corpus. Push the output to the platform record (local
type) per deploy/COMPUTE.md's stored-yaml procedure.
"""
import sys
import yaml

src = sys.argv[1] if len(sys.argv) > 1 else 'deploy/workflow.yaml'
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
ins['session_settings']['items']['session_name']['tooltip'] = (
    'Platform session for the web interface, per user, kept across relaunches. '
    'Change it to run more than one studio.')

yaml.safe_dump(d, sys.stdout, sort_keys=False, width=100, allow_unicode=True)
