// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! Sets of id groups that iterate and hash as CPython 3.13's frozenset does.
//!
//! The groups of an id property are written into a merged file in the iteration order of a
//! CPython frozenset of them, and the name of a split merge ends in CPython's hash of its ids. A
//! set therefore keeps its table the way CPython's set object keeps it (Objects/setobject.c): the
//! slots, the probe sequence, the deleted slots and the resizes, and each operation builds its
//! result in the steps of the CPython function named in its documentation.

use crate::hash;

/// The size of a new table, `PySet_MINSIZE`.
const MINIMUM_SLOTS: usize = 8;

/// The number of neighbouring slots probed after each perturbed start, `LINEAR_PROBES`.
const LINEAR_PROBES: usize = 9;

/// `PERTURB_SHIFT`.
const PERTURB_SHIFT: u32 = 5;

/// The values of one entry of an id property, such as a platform's msm id and version: a tuple of
/// integers in CPython. A group has at least one value.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Group(Vec<i64>);

impl Group {
    /// A group of these values, None for no values.
    pub(crate) fn new(values: &[i64]) -> Option<Self> {
        (!values.is_empty()).then(|| Self(values.to_vec()))
    }

    /// The values in their order.
    pub fn values(&self) -> &[i64] {
        &self.0
    }

    fn hash(&self) -> u64 {
        hash::tuple(&self.0.iter().map(|&value| hash::int(value)).collect::<Vec<_>>())
    }
}

#[derive(Clone, Debug)]
struct Entry {
    hash: u64,
    group: Group,
}

impl Entry {
    fn new(group: Group) -> Self {
        Self { hash: group.hash(), group }
    }

    fn matches(&self, other: &Entry) -> bool {
        self.hash == other.hash && self.group == other.group
    }
}

#[derive(Clone, Debug)]
enum Slot {
    Unused,
    /// A slot whose entry was discarded; CPython's dummy entry.
    Deleted,
    Active(Entry),
}

/// Where the probe for an entry ends: at the slot of a matching entry, or at an unused slot, with
/// the last deleted slot passed before it.
enum Probed {
    Found(usize),
    Absent { unused: usize, deleted: Option<usize> },
}

/// The slots CPython visits for a hash, in runs: a run is its start and, when they all lie inside
/// the table, the nine slots after it, and the next run starts at a slot computed from this run's
/// start and the perturbed hash. The sequence never ends, and a table always has an unused slot
/// to end a search.
struct Probe {
    mask: usize,
    perturb: usize,
    start: usize,
    offset: usize,
}

impl Probe {
    fn new(hash: u64, mask: usize) -> Self {
        // CPython's (size_t)hash; the hash module restricts merge_dtbs to 64-bit hosts.
        let hash = hash as usize;
        Self { mask, perturb: hash, start: hash & mask, offset: 0 }
    }

    fn next_slot(&mut self) -> usize {
        let run_length =
            if self.start + LINEAR_PROBES <= self.mask { LINEAR_PROBES + 1 } else { 1 };
        if self.offset == run_length {
            self.perturb >>= PERTURB_SHIFT;
            self.start =
                self.start.wrapping_mul(5).wrapping_add(1).wrapping_add(self.perturb) & self.mask;
            self.offset = 0;
        }
        let slot = self.start + self.offset;
        self.offset += 1;
        slot
    }
}

/// A set of groups. The operations that make a set give None instead of an empty one.
#[derive(Clone, Debug)]
pub struct GroupSet {
    slots: Vec<Slot>,
    /// Active and deleted slots.
    fill: usize,
    /// Active slots.
    used: usize,
}

impl GroupSet {
    /// `frozenset(groups)`, the groups added in their order; None for no groups.
    pub fn from_groups(groups: impl IntoIterator<Item = Group>) -> Option<Self> {
        let mut set = Self::empty();
        for group in groups {
            set.add(Entry::new(group));
        }
        set.non_empty()
    }

    /// The number of groups.
    pub fn len(&self) -> usize {
        self.used
    }

    /// The groups in CPython's iteration order, `set_next`: by slot.
    pub fn iter(&self) -> impl Iterator<Item = &Group> {
        self.entries().map(|entry| &entry.group)
    }

    /// Whether the set has this group.
    #[cfg(test)]
    pub fn contains(&self, group: &Group) -> bool {
        self.has(&Entry::new(group.clone()))
    }

    /// `self & other`, `set_intersection`: the groups both sets have, collected from the smaller
    /// set, or from `other` for sets of equal size, in its order. None when they share none.
    pub fn intersection(&self, other: &GroupSet) -> Option<GroupSet> {
        let (searched, collected) =
            if other.len() > self.len() { (other, self) } else { (self, other) };
        let mut result = Self::empty();
        for entry in collected.entries().filter(|entry| searched.has(entry)) {
            result.add(entry.clone());
        }
        result.non_empty()
    }

    /// `self - other`, `set_difference`: the groups that `other` lacks, or None when there are
    /// none. A set whose length divided by four, rounded down, is larger than the length of
    /// `other` is copied and has the groups of `other` discarded, as `set_copy_and_difference`
    /// does; otherwise its groups that `other` lacks are collected in its order.
    pub fn difference(&self, other: &GroupSet) -> Option<GroupSet> {
        if self.len() >> 2 > other.len() {
            // set_difference_update_internal replaces an `other` whose length divided by eight,
            // rounded down, is larger than the length of the copy by their intersection, which
            // cannot happen for this smaller `other`.
            let mut result = self.copy();
            for entry in other.entries() {
                result.discard(entry);
            }
            if result.fill - result.used > result.mask() / 4 {
                result.resize(result.growth_target());
            }
            return result.non_empty();
        }
        let mut result = Self::empty();
        for entry in self.entries().filter(|entry| !other.has(entry)) {
            result.add(entry.clone());
        }
        result.non_empty()
    }

    /// Whether `other` has every group of this set.
    pub fn is_subset(&self, other: &GroupSet) -> bool {
        self.len() <= other.len() && self.entries().all(|entry| other.has(entry))
    }

    /// Whether the two sets share a group.
    pub fn intersects(&self, other: &GroupSet) -> bool {
        self.entries().any(|entry| other.has(entry))
    }

    /// The hash of the frozenset, `frozenset_hash`.
    pub fn hash(&self) -> u64 {
        hash::frozenset(self.entries().map(|entry| entry.hash), self.used)
    }

    fn empty() -> Self {
        Self { slots: vec![Slot::Unused; MINIMUM_SLOTS], fill: 0, used: 0 }
    }

    fn non_empty(self) -> Option<Self> {
        (self.used > 0).then_some(self)
    }

    fn mask(&self) -> usize {
        self.slots.len() - 1
    }

    fn entries(&self) -> impl Iterator<Item = &Entry> {
        self.slots.iter().filter_map(|slot| match slot {
            Slot::Active(entry) => Some(entry),
            Slot::Unused | Slot::Deleted => None,
        })
    }

    /// The probe of `set_lookkey` and `set_add_entry`.
    fn probe_for(&self, entry: &Entry) -> Probed {
        let mut probe = Probe::new(entry.hash, self.mask());
        let mut deleted = None;
        loop {
            let index = probe.next_slot();
            match &self.slots[index] {
                Slot::Unused => return Probed::Absent { unused: index, deleted },
                Slot::Deleted => deleted = Some(index),
                Slot::Active(active) if active.matches(entry) => return Probed::Found(index),
                Slot::Active(_) => {}
            }
        }
    }

    fn has(&self, entry: &Entry) -> bool {
        matches!(self.probe_for(entry), Probed::Found(_))
    }

    /// `set_add_entry`: an entry goes into the last deleted slot its probe passed, or else into
    /// the unused slot that ended the probe, which can grow the table.
    fn add(&mut self, entry: Entry) {
        match self.probe_for(&entry) {
            Probed::Found(_) => {}
            Probed::Absent { deleted: Some(index), .. } => {
                self.slots[index] = Slot::Active(entry);
                self.used += 1;
            }
            Probed::Absent { unused, deleted: None } => {
                self.slots[unused] = Slot::Active(entry);
                self.fill += 1;
                self.used += 1;
                if self.fill * 5 >= self.mask() * 3 {
                    self.resize(self.growth_target());
                }
            }
        }
    }

    /// `set_discard_entry`.
    fn discard(&mut self, entry: &Entry) {
        if let Probed::Found(index) = self.probe_for(entry) {
            self.slots[index] = Slot::Deleted;
            self.used -= 1;
        }
    }

    /// The minimum number of active slots a grown table is sized for.
    fn growth_target(&self) -> usize {
        if self.used > 50_000 {
            self.used * 2
        } else {
            self.used * 4
        }
    }

    /// `set_table_resize`: a table of the smallest power of two above `minimum_used` slots, eight
    /// at least, with the active entries inserted in slot order. A smallest table without deleted
    /// slots stays as it is.
    fn resize(&mut self, minimum_used: usize) {
        let size = (minimum_used + 1).next_power_of_two().max(MINIMUM_SLOTS);
        if size == MINIMUM_SLOTS && self.slots.len() == MINIMUM_SLOTS && self.fill == self.used {
            return;
        }
        let old = std::mem::replace(&mut self.slots, vec![Slot::Unused; size]);
        self.fill = self.used;
        for slot in old {
            if let Slot::Active(entry) = slot {
                self.insert_clean(entry);
            }
        }
    }

    /// `set_insert_clean`: an entry into the first unused slot of its probe, in a table without
    /// deleted slots. The caller counts it.
    fn insert_clean(&mut self, entry: Entry) {
        let mut probe = Probe::new(entry.hash, self.mask());
        loop {
            let index = probe.next_slot();
            if let Slot::Unused = self.slots[index] {
                self.slots[index] = Slot::Active(entry);
                return;
            }
        }
    }

    /// `set_copy`: a new set filled by `set_merge_lock_held`, which sizes the table for the
    /// entries first, then copies the slots as they are when both tables have the same size and
    /// the source has no deleted slots, and inserts the entries in slot order otherwise.
    fn copy(&self) -> GroupSet {
        let mut copy = Self::empty();
        if (copy.fill + self.used) * 5 >= copy.mask() * 3 {
            copy.resize((copy.used + self.used) * 2);
        }
        copy.fill = self.used;
        copy.used = self.used;
        if copy.mask() == self.mask() && self.fill == self.used {
            copy.slots.clone_from(&self.slots);
        } else {
            for entry in self.entries() {
                copy.insert_clean(entry.clone());
            }
        }
        copy
    }
}

impl PartialEq for GroupSet {
    fn eq(&self, other: &Self) -> bool {
        self.len() == other.len() && self.is_subset(other)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The set of two-value groups of these values, in this order.
    fn pairs(values: &[i64]) -> GroupSet {
        GroupSet::from_groups(values.chunks(2).filter_map(Group::new)).unwrap()
    }

    // The expected orders are those of CPython 3.13 for the same sets and operations.

    fn order(set: &GroupSet) -> Vec<[i64; 2]> {
        set.iter().map(|group| <[i64; 2]>::try_from(group.values()).unwrap()).collect()
    }

    fn flat(groups: &[[i64; 2]]) -> Vec<i64> {
        groups.iter().flatten().copied().collect()
    }

    #[test]
    fn construction_order() {
        let set = pairs(&[457, 65536, 482, 65536, 457, 131072, 482, 131072, 552, 65536]);
        assert_eq!(
            order(&set),
            [[482, 131072], [457, 65536], [457, 131072], [552, 65536], [482, 65536]]
        );
        let set = pairs(&[530, 65536, 531, 65536, 540, 65536]);
        assert_eq!(order(&set), [[531, 65536], [540, 65536], [530, 65536]]);
        // A repeated group is in the set once.
        assert_eq!(pairs(&[1, 0, 1, 0]).len(), 1);
        assert!(GroupSet::from_groups([]).is_none());
    }

    #[test]
    fn groups_are_not_empty() {
        assert!(Group::new(&[]).is_none());
        assert_eq!(Group::new(&[0]).unwrap().values(), [0]);
    }

    #[test]
    fn grown_table_order() {
        let values: Vec<i64> = (1..=20).flat_map(|i| [i * 3, 0]).collect();
        assert_eq!(
            flat(&order(&pairs(&values))),
            flat(&[
                [30, 0],
                [21, 0],
                [54, 0],
                [12, 0],
                [45, 0],
                [3, 0],
                [36, 0],
                [27, 0],
                [60, 0],
                [51, 0],
                [18, 0],
                [9, 0],
                [42, 0],
                [33, 0],
                [24, 0],
                [57, 0],
                [15, 0],
                [48, 0],
                [6, 0],
                [39, 0],
            ])
        );
    }

    #[test]
    fn intersection_of_equal_sizes_follows_the_right_operand() {
        let a = pairs(&[540, 131072, 552, 65536, 457, 0, 415, 131072, 519, 131072]);
        let b = pairs(&[415, 131072, 540, 131072, 482, 65536, 552, 65536, 552, 131072]);
        let a_and_b = a.intersection(&b).unwrap();
        let b_and_a = b.intersection(&a).unwrap();
        assert_eq!(order(&a_and_b), [[415, 131072], [540, 131072], [552, 65536]]);
        assert_eq!(order(&b_and_a), [[540, 131072], [415, 131072], [552, 65536]]);
        assert!(a_and_b == b_and_a);
    }

    #[test]
    fn intersection_follows_the_smaller_set() {
        let larger =
            pairs(&[457, 131072, 591, 0, 531, 65536, 530, 0, 482, 131072, 457, 0, 457, 65536]);
        let smaller = pairs(&[457, 65536, 591, 0, 531, 131072, 457, 131072]);
        let expected = [[591, 0], [457, 131072], [457, 65536]];
        assert_eq!(order(&larger.intersection(&smaller).unwrap()), expected);
        assert_eq!(order(&smaller.intersection(&larger).unwrap()), expected);
        assert!(larger.intersection(&pairs(&[9, 9])).is_none());
    }

    #[test]
    fn difference_collects_in_order() {
        let a = pairs(&[100, 0, 200, 0, 300, 0, 400, 0, 500, 0, 600, 0]);
        let b = pairs(&[600, 0, 500, 0, 400, 0, 900, 0]);
        assert_eq!(order(&a.difference(&b).unwrap()), [[300, 0], [200, 0], [100, 0]]);
        assert!(b.difference(&pairs(&[600, 0, 500, 0, 400, 0, 900, 0])).is_none());
    }

    #[test]
    fn difference_of_a_much_larger_set_discards_from_a_copy() {
        let values: Vec<i64> = (1..=12).flat_map(|i| [i * 7, i]).collect();
        let set = pairs(&values);
        let difference = set.difference(&pairs(&[14, 2, 35, 5])).unwrap();
        assert_eq!(
            flat(&order(&difference)),
            flat(&[
                [49, 7],
                [70, 10],
                [7, 1],
                [63, 9],
                [77, 11],
                [21, 3],
                [42, 6],
                [84, 12],
                [56, 8],
                [28, 4],
            ])
        );
        // More discarded slots than a quarter of the table resize it.
        let values: Vec<i64> = (1..=20).flat_map(|i| [i * 3, 0]).collect();
        let difference = pairs(&values).difference(&pairs(&[3, 0, 6, 0, 9, 0, 12, 0])).unwrap();
        assert_eq!(
            flat(&order(&difference)),
            flat(&[
                [18, 0],
                [30, 0],
                [42, 0],
                [21, 0],
                [54, 0],
                [33, 0],
                [45, 0],
                [24, 0],
                [57, 0],
                [36, 0],
                [15, 0],
                [48, 0],
                [27, 0],
                [60, 0],
                [39, 0],
                [51, 0],
            ])
        );
    }

    #[test]
    fn add_reuses_a_discarded_slot() {
        let mut set = pairs(&[531, 65536, 591, 0, 540, 65536, 552, 0]);
        assert_eq!(order(&set), [[552, 0], [531, 65536], [540, 65536], [591, 0]]);
        set.discard(&Entry::new(Group::new(&[531, 65536]).unwrap()));
        set.add(Entry::new(Group::new(&[591, 65536]).unwrap()));
        assert_eq!(order(&set), [[552, 0], [591, 65536], [540, 65536], [591, 0]]);
    }

    #[test]
    fn comparisons() {
        let set = pairs(&[1, 0, 2, 0]);
        assert!(set == pairs(&[2, 0, 1, 0]));
        assert!(set != pairs(&[1, 0]));
        assert!(pairs(&[1, 0]).is_subset(&set));
        assert!(!set.is_subset(&pairs(&[1, 0, 3, 0])));
        assert!(set.intersects(&pairs(&[2, 0, 3, 0])));
        assert!(!set.intersects(&pairs(&[3, 0])));
        assert!(set.contains(&Group::new(&[2, 0]).unwrap()));
        assert!(!set.contains(&Group::new(&[0, 2]).unwrap()));
    }

    #[test]
    fn frozenset_hash() {
        assert_eq!(pairs(&[1, 0]).hash(), 2_658_973_335_701_269_396);
        assert_eq!(pairs(&[2, 0, 1, 0]).hash(), 9_098_881_018_260_531_682);
        // Discarded slots do not count.
        let values: Vec<i64> = (1..=20).flat_map(|i| [i * 3, 0]).collect();
        let difference = pairs(&values).difference(&pairs(&[3, 0, 6, 0, 9, 0, 12, 0])).unwrap();
        let collected: Vec<i64> = (5..=20).flat_map(|i| [i * 3, 0]).collect();
        assert_eq!(difference.hash(), pairs(&collected).hash());
    }
}
