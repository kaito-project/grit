#!/bin/bash

src="/mnt/pvc-data/test50GB"
dest="/mnt/host-data/test50GB_copy"
iterations=${1:-5}
file_size=53687091200
total_speed=0

echo "start copy test: from $src to $dest $iterations times"

for i in $(seq 1 $iterations); do
    echo "-------------------------------"
    echo "test number $i ："
    rm -f "$dest"
    
    start=$(date +%s.%N)
    #dd if=$src of=$dest bs=1M conv=noerror,sync
    cp $src $dest
    end=$(date +%s.%N)
    
    duration=$(echo "$end - $start" | bc -l)
    
    # speed unit: MB/s：speed = file_size / duration / (1024*1024)
    speed=$(echo "scale=2; $file_size / $duration / (1024*1024)" | bc -l)
    
    echo "copy real duration：$duration seconds"
    echo "copy speed：$speed MB/s"
    
    total_speed=$(echo "$total_speed + $speed" | bc -l)
done

average_speed=$(echo "scale=2; $total_speed / $iterations" | bc -l)
echo "-------------------------------"
echo "average copy speed for $iterations times：$average_speed MB/s"
