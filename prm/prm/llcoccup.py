# Copyright (C) 2018 Intel Corporation
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions
# and limitations under the License.
#
#
# SPDX-License-Identifier: Apache-2.0

""" This module implements last level cache control based on pqos tool """

from __future__ import print_function

import logging
from prm.resource import Resource

from owca.allocators import AllocationType, RDTAllocation

log = logging.getLogger(__name__)


class LlcOccup(Resource):
    """ This class is the resource class of LLC occupancy """

    def __init__(self, exclusive):
        self.be_bmp = []
        self.lc_bmp = []
        self.cur_allocs = None
        self.new_allocs = None
        self.exclusive = exclusive
        super(LlcOccup, self).__init__()

    def update_allocs(self, cur_allocs, new_allocs, cbm_mask, nsocks):
        self.cur_allocs = cur_allocs
        self.new_allocs = new_allocs
        self.nsocks = nsocks
        if not self.be_bmp:
            bitcnt = LlcOccup._get_cbm_bit_count(cbm_mask)
            self.be_bmp = [hex(((1 << (i + 1)) - 1) << (bitcnt - 1 - i))
                           for i in range(1, bitcnt)]
            self.lc_bmp = [hex((1 << (bitcnt - 1 - i)) - 1)
                           for i in range(1, bitcnt)]
            if self.exclusive:
                self.be_bmp = self.be_bmp[0:int(bitcnt / 2)]
                self.lc_bmp = self.lc_bmp[0:int(bitcnt / 2)]
            self.level_max = int(bitcnt / 2) - 2 if self.exclusive else bitcnt - 2

    @staticmethod
    def _get_cbm_bit_count(cbm_mask):
        cbm = int(cbm_mask, 16)
        cbm_bin = bin(cbm)
        setbits = [bit for bit in cbm_bin[2:] if bit == '1']
        return len(setbits)

    def _set_alloc(self, task_id, name, alloc: str):
        is_new = True
        if task_id in self.cur_allocs and\
            AllocationType.RDT in self.cur_allocs[task_id] and\
                alloc == self.cur_allocs[task_id][AllocationType.RDT].l3:
            is_new = False

        if is_new:
            if task_id in self.new_allocs:
                task_allocs = self.new_allocs[task_id]
            else:
                task_allocs = dict()
                self.new_allocs[task_id] = task_allocs
            if AllocationType.RDT in task_allocs:
                rdt_alloc = task_allocs[AllocationType.RDT]
            else:
                rdt_alloc = RDTAllocation()
                task_allocs[AllocationType.RDT] = rdt_alloc
            rdt_alloc.name = name
            rdt_alloc.l3 = alloc

    def _budgeting(self, cid, is_be):
        if is_be:
            bmp = self.be_bmp
            name = 'BE_Group'
        else:
            bmp = self.lc_bmp
            name = 'LC_Group'
        l3s = [str(idx) + '=' + bmp[self.quota_level] for idx in range(self.nsocks)]
        l3_allocs = 'L3:' + ';'.join(l3s)
        self._set_alloc(cid, name, l3_allocs)

    def budgeting(self, bes, lcs):
        if bes:
            for cid in bes:
                self._budgeting(cid, True)
            log.info('set container ' + ','.join(bes) + ' llc occupancy to ' +
                     self.be_bmp[self.quota_level])
        if lcs:
            for cid in lcs:
                self._budgeting(cid, False)
            log.info('set container ' + ','.join(lcs) + ' llc occupancy to ' +
                     self.lc_bmp[self.quota_level])
