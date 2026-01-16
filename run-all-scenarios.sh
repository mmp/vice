#!/bin/bash

# run-all-scenarios.sh
# Runs vice -runsim for all available scenarios

set -euo pipefail

# Function to handle cleanup on script exit
cleanup() {
    echo ""
    echo -e "\033[0;33mScript interrupted or terminated\033[0m"
    exit 130
}

# Set up signal handlers for Ctrl-C and other termination signals
trap cleanup SIGINT SIGTERM

# Check for executable path argument
if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <path-to-vice-executable>"
    exit 1
fi

# Configuration
VICE_BINARY="$1"
LOG_FILE="scenario-test-full.txt"
SUMMARY_FILE="scenario-test-summary.txt"
WARNINGS_FILE="scenario-test-warnings.txt"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
ORANGE='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Counters
total_scenarios=0
successful_scenarios=0
failed_scenarios=0

# Arrays to track results
declare -a failed_list=()
declare -a successful_list=()

# Function to log with timestamp
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

# Function to print colored output
print_status() {
    local color=$1
    local message=$2
    echo -e "${color}${message}${NC}"
}

# Check if vice binary exists
if [[ ! -x "$VICE_BINARY" ]]; then
    print_status "$RED" "Error: vice binary not found or not executable at $VICE_BINARY"
    exit 1
fi

# Initialize log files
{
    echo "Vice Scenario Test Log"
    echo "====================="
    echo "Started: $(date)"
    echo ""
} > "$LOG_FILE"

# Clear warnings file
> "$WARNINGS_FILE"

# Get list of all scenarios
print_status "$BLUE" "Getting list of all scenarios..."
listscenarios_stderr=$(mktemp)
if ! scenarios_raw=$("$VICE_BINARY" -listscenarios 2>"$listscenarios_stderr"); then
    print_status "$RED" "Error: Failed to get scenario list from vice -listscenarios"
    print_status "$RED" "stderr output:"
    cat "$listscenarios_stderr"
    rm -f "$listscenarios_stderr"
    exit 1
fi
rm -f "$listscenarios_stderr"

# Convert to array, handling spaces properly (portable approach)
IFS=$'\n' read -d '' -r -a scenarios <<< "$scenarios_raw" || true

total_scenarios=${#scenarios[@]}
print_status "$BLUE" "Found $total_scenarios scenarios to test"

# Initialize summary file
{
    echo "Vice Scenario Test Summary"
    echo "========================="
    echo "Started: $(date)"
    echo "Total scenarios: $total_scenarios"
    echo ""
} > "$SUMMARY_FILE"

# Main loop - process each scenario
for i in "${!scenarios[@]}"; do
    scenario="${scenarios[$i]}"
    
    # Skip empty lines
    [[ -z "$scenario" ]] && continue
    
    scenario_num=$((i + 1))
    log "[$scenario_num/$total_scenarios] Testing scenario: $scenario"
    
    # Log scenario start to main log file
    {
        echo "=================================================================================="
        echo "[$scenario_num/$total_scenarios] Testing scenario: $scenario"
        echo "Started: $(date)"
        echo "=================================================================================="
    } >> "$LOG_FILE"
    
    # Run the scenario with timeout
    start_time=$(date +%s)
    if timeout 1000 "$VICE_BINARY" -runsim "$scenario" >> "$LOG_FILE" 2>&1; then
        end_time=$(date +%s)
        duration=$((end_time - start_time))
        
        # Extract aircraft count from log
        aircraft_spawned=$(grep "ended with" "$LOG_FILE" | tail -1 | sed 's/.*ended with \([0-9]*\).*/\1/' || echo "unknown")
        
        # Check for warnings/errors in the scenario output
        scenario_start_line=$(grep -n -F "Testing scenario: $scenario" "$LOG_FILE" | tail -1 | cut -d: -f1)
        if [[ -n "$scenario_start_line" ]]; then
            # Look for warnings/errors from this scenario's start to end of file
            warnings_found=$(tail -n +$scenario_start_line "$LOG_FILE" | grep -E "(WARN|ERROR|error|warning)" || true)
            if [[ -n "$warnings_found" ]]; then
                {
                    echo "==================== $scenario ===================="
                    echo "$warnings_found"
                    echo ""
                } >> "$WARNINGS_FILE"
            fi
        fi
        
        successful_scenarios=$((successful_scenarios + 1))
        successful_list+=("$scenario")
        print_status "$GREEN" "  ✓ SUCCESS ($duration seconds, $aircraft_spawned aircraft spawned)"
        
        # Log success to summary
        echo "✓ $scenario - SUCCESS ($duration seconds, $aircraft_spawned aircraft)" >> "$SUMMARY_FILE"
    else
        exit_code=$?
        end_time=$(date +%s)
        duration=$((end_time - start_time))
        
        # Failed scenarios always have warnings/errors - capture the failure details
        scenario_start_line=$(grep -n -F "Testing scenario: $scenario" "$LOG_FILE" | tail -1 | cut -d: -f1)
        if [[ -n "$scenario_start_line" ]]; then
            # Get the error output from this scenario
            error_output=$(tail -n +$scenario_start_line "$LOG_FILE" | tail -10)
            {
                echo "==================== $scenario (FAILED) ===================="
                echo "$error_output"
                echo ""
            } >> "$WARNINGS_FILE"
        else
            {
                echo "==================== $scenario (FAILED) ===================="
                echo "Failed to capture error output"
                echo ""
            } >> "$WARNINGS_FILE"
        fi
        
        failed_scenarios=$((failed_scenarios + 1))
        failed_list+=("$scenario")
        
        if [[ $exit_code -eq 124 ]]; then
            print_status "$RED" "  ✗ TIMEOUT (>300 seconds)"
            echo "✗ $scenario - TIMEOUT (>300 seconds)" >> "$SUMMARY_FILE"
        else
            print_status "$RED" "  ✗ FAILED (exit code $exit_code after $duration seconds)"
            echo "✗ $scenario - FAILED (exit code $exit_code after $duration seconds)" >> "$SUMMARY_FILE"
        fi
        
        # Show last few lines of error log from main log file
        print_status "$ORANGE" "    Last error lines:"
        tail -3 "$LOG_FILE" | sed 's/^/    /'
    fi
done

# Final summary
end_time=$(date)
{
    echo ""
    echo "Completed: $end_time"
    echo ""
    echo "FINAL RESULTS:"
    echo "=============="
    echo "Total scenarios: $total_scenarios"
    echo "Successful: $successful_scenarios"
    echo "Failed: $failed_scenarios"
    echo "Success rate: $(( successful_scenarios * 100 / total_scenarios ))%"
} >> "$SUMMARY_FILE"

print_status "$BLUE" ""
print_status "$BLUE" "==================== FINAL SUMMARY ===================="
print_status "$BLUE" "Total scenarios tested: $total_scenarios"
print_status "$GREEN" "Successful: $successful_scenarios"
print_status "$RED" "Failed: $failed_scenarios"

if [[ $failed_scenarios -gt 0 ]]; then
    success_rate=$(( successful_scenarios * 100 / total_scenarios ))
    print_status "$ORANGE" "Success rate: $success_rate%"
    
    echo "" >> "$SUMMARY_FILE"
    echo "FAILED SCENARIOS:" >> "$SUMMARY_FILE"
    printf '%s\n' "${failed_list[@]}" >> "$SUMMARY_FILE"
    
    print_status "$RED" ""
    print_status "$RED" "Failed scenarios:"
    printf '  %s\n' "${failed_list[@]}"
else
    print_status "$GREEN" "Success rate: 100% - All scenarios passed!"
fi

print_status "$BLUE" ""
print_status "$BLUE" "Detailed logs saved in: $LOG_FILE"
print_status "$BLUE" "Summary saved in: $SUMMARY_FILE"

# Report scenarios with warnings/errors
if [[ -s "$WARNINGS_FILE" ]]; then
    warning_count=$(grep -c "====" "$WARNINGS_FILE")
    print_status "$ORANGE" ""
    print_status "$ORANGE" "==================== WARNING/ERROR REPORT ===================="
    print_status "$ORANGE" "Scenarios with warnings or errors: $warning_count"
    print_status "$ORANGE" ""
    
    # Show just the scenario names for console output
    grep "====" "$WARNINGS_FILE" | sed 's/.*== \(.*\) ==.*/  \1/' | while read -r scenario_name; do
        print_status "$ORANGE" "$scenario_name"
    done
    
    print_status "$ORANGE" ""
    print_status "$ORANGE" "Full warning/error details saved in: $WARNINGS_FILE"
    
    # Add to summary file
    {
        echo ""
        echo "SCENARIOS WITH WARNINGS/ERRORS:"
        echo "================================"
        cat "$WARNINGS_FILE"
    } >> "$SUMMARY_FILE"
else
    print_status "$GREEN" ""
    print_status "$GREEN" "No warnings or errors detected in any scenarios!"
fi

# Exit with appropriate code
if [[ $failed_scenarios -gt 0 ]]; then
    exit 1
else
    exit 0
fi
