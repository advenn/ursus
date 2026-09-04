"""Benchmark driver for the ursus dataframe library.

The driver never runs a query itself. It generates data, launches one engine
subprocess per (engine, query) pair under an enforced resource budget, collects
the JSON each runner emits, validates results against a duckdb reference, and
renders the report. Keeping the driver out of the timed path is what makes the
Python and Go engines comparable.
"""
