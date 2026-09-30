// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! An empty directory for one test, removed with everything in it when the test ends.

use std::path::{Path, PathBuf};
use std::{env, fs, process};

pub struct ScratchDir(PathBuf);

impl ScratchDir {
    /// A new directory below the temporary directory, named after the test.
    pub fn new(test: &str) -> Self {
        let path = env::temp_dir().join(format!("merge_dtbs_test-{}-{test}", process::id()));
        if path.exists() {
            fs::remove_dir_all(&path).unwrap();
        }
        fs::create_dir_all(&path).unwrap();
        Self(path)
    }

    pub fn path(&self) -> &Path {
        &self.0
    }

    /// A new directory below this one.
    pub fn subdirectory(&self, name: &str) -> PathBuf {
        let path = self.0.join(name);
        fs::create_dir_all(&path).unwrap();
        path
    }
}

impl Drop for ScratchDir {
    fn drop(&mut self) {
        // A failed removal leaves files in the temporary directory and fails no test.
        let _ = fs::remove_dir_all(&self.0);
    }
}
