// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! CPython 3.13's hash functions for the values merge_dtbs hashes: integers, None, tuples and
//! frozensets. Hashes are 64 bits wide, as CPython's are on 64-bit hosts, and computed as unsigned
//! values; CPython's signed hash is their two's complement reading.

#[cfg(not(target_pointer_width = "64"))]
compile_error!("merge_dtbs reproduces CPython's 64-bit hashes and set tables");

/// The modulus of CPython's integer hash, 2**61 - 1.
const INT_MODULUS: u64 = (1 << 61) - 1;

const XXPRIME_1: u64 = 11_400_714_785_074_694_791;
const XXPRIME_2: u64 = 14_029_467_366_897_019_727;
const XXPRIME_5: u64 = 2_870_177_450_012_600_261;

/// The hash of None.
pub const NONE: u64 = 0xFCA8_6420;

/// The hash of an integer. -1 is CPython's error value, so -1 hashes to -2.
pub fn int(value: i64) -> u64 {
    let magnitude = value.unsigned_abs() % INT_MODULUS;
    let hash = if value < 0 { magnitude.wrapping_neg() } else { magnitude };
    if hash == u64::MAX {
        u64::MAX - 1
    } else {
        hash
    }
}

/// The hash of a tuple of items with these hashes.
pub fn tuple(item_hashes: &[u64]) -> u64 {
    let mut acc = item_hashes.iter().fold(XXPRIME_5, |acc, &lane| {
        acc.wrapping_add(lane.wrapping_mul(XXPRIME_2)).rotate_left(31).wrapping_mul(XXPRIME_1)
    });
    acc = acc.wrapping_add(item_hashes.len() as u64 ^ (XXPRIME_5 ^ 3_527_539));
    if acc == u64::MAX {
        1_546_275_796
    } else {
        acc
    }
}

/// The hash of a frozenset with elements of these hashes.
pub fn frozenset(element_hashes: impl Iterator<Item = u64>, len: usize) -> u64 {
    let shuffled = |hash: u64| ((hash ^ 89_869_747) ^ (hash << 16)).wrapping_mul(3_644_798_167);
    let mut hash = element_hashes.fold(0, |acc, element| acc ^ shuffled(element));
    hash ^= (len as u64 + 1).wrapping_mul(1_927_868_237);
    hash ^= (hash >> 11) ^ (hash >> 25);
    hash = hash.wrapping_mul(69_069).wrapping_add(907_133_923);
    if hash == u64::MAX {
        590_923_713
    } else {
        hash
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The expected hashes are CPython 3.13's, read as unsigned values.

    #[test]
    fn int_hashes() {
        for (value, expected) in [
            (0, 0),
            (5, 5),
            (-5, 18_446_744_073_709_551_611),
            (-1, 18_446_744_073_709_551_614),
            (-2, 18_446_744_073_709_551_614),
            ((1 << 61) - 1, 0),
            (1 << 61, 1),
            (-((1 << 61) - 1), 0),
            (i64::MAX, 3),
            (i64::MIN, 18_446_744_073_709_551_612),
        ] {
            assert_eq!(int(value), expected, "hash({value})");
        }
    }

    #[test]
    fn none_hash() {
        assert_eq!(NONE, 4_238_894_112);
    }

    #[test]
    fn tuple_hashes() {
        let tuple_of =
            |values: &[i64]| tuple(&values.iter().map(|&value| int(value)).collect::<Vec<_>>());
        assert_eq!(tuple_of(&[]), 5_740_354_900_026_072_187);
        assert_eq!(tuple_of(&[1]), 11_802_529_618_835_948_721);
        assert_eq!(tuple_of(&[1, 0]), 13_282_122_221_094_607_640);
        assert_eq!(tuple_of(&[457, 65536]), 16_021_635_012_203_317_878);
        assert_eq!(tuple_of(&[-1, -1]), 960_947_337_673_586_213);
        assert_eq!(tuple_of(&[1, 2, 3, 4]), 590_899_387_183_067_792);
    }

    #[test]
    fn frozenset_hashes() {
        let pair = |a: i64, b: i64| tuple(&[int(a), int(b)]);
        assert_eq!(frozenset([pair(1, 0)].into_iter(), 1), 2_658_973_335_701_269_396);
        // The order of the elements does not count.
        for elements in [[pair(1, 0), pair(2, 0)], [pair(2, 0), pair(1, 0)]] {
            assert_eq!(frozenset(elements.into_iter(), 2), 9_098_881_018_260_531_682);
        }
        let quadruple = tuple(&[int(1), int(2), int(3), int(4)]);
        assert_eq!(frozenset([quadruple].into_iter(), 1), 5_713_365_361_416_576_826);
    }
}
